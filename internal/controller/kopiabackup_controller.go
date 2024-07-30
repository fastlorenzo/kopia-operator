/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"path/filepath"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/v1alpha1"
	"github.com/go-logr/logr"
)

// KopiaBackupReconciler reconciles a KopiaBackup object
type KopiaBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the KopiaBackup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kopiabackup", req.NamespacedName)

	// Fetch the KopiaBackup instance
	backup := &backupv1alpha1.KopiaBackup{}
	err := r.Get(ctx, req.NamespacedName, backup)
	if err != nil {
		log.Error(err, "unable to fetch KopiaBackup")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the repository exists (KopiaRepository) - name is in backup.Spec.Repository
	repository := &backupv1alpha1.KopiaRepository{}
	err = r.Get(ctx, types.NamespacedName{Name: backup.Spec.Repository, Namespace: backup.Namespace}, repository)
	if err != nil {
		log.Error(err, "unable to fetch KopiaRepository")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Found KopiaRepository", "KopiaRepository", repository.Name)

	// List all Pods in the same namespace
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(backup.Namespace)); err != nil {
		log.Error(err, "unable to list pods in the namespace")
		return ctrl.Result{}, err
	}

	var nodeName string
	var appName string // label app.kubernetes.io/name
	for _, pod := range podList.Items {
		// Check if pod is running and has the PVC mounted
		if pod.Status.Phase == corev1.PodRunning {
			for _, volume := range pod.Spec.Volumes {
				if pvc := volume.PersistentVolumeClaim; pvc != nil && pvc.ClaimName == backup.Spec.PVCName {
					nodeName = pod.Spec.NodeName
					// Check if the pod has the label app.kubernetes.io/name
					appName = pod.Labels["app.kubernetes.io/name"]
					break
				}
			}
		}
		if nodeName != "" {
			break
		}
	}

	if nodeName == "" {
		log.Info("No running pod found with the PVC mounted, requeuing")
		return ctrl.Result{Requeue: true}, nil
	}

	log.Info("Found node with running pod", "node", nodeName)

	// Create or update the CronJob for the backup
	cronJob := constructCronJob(backup, nodeName, appName, repository)
	if cronJob == nil {
		log.Error(nil, "constructCronJob returned nil")
		return ctrl.Result{}, fmt.Errorf("constructCronJob returned nil")
	}

	if err := ctrl.SetControllerReference(backup, cronJob, r.Scheme); err != nil {
		log.Error(err, "unable to set owner reference on cronJob")
		return ctrl.Result{}, err
	}

	// Logic to create or update the CronJob
	found := &batchv1.CronJob{}
	err = r.Get(ctx, types.NamespacedName{Name: cronJob.Name, Namespace: cronJob.Namespace}, found)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating a new CronJob", "CronJob.Namespace", cronJob.Namespace, "CronJob.Name", cronJob.Name)
			err = r.Create(ctx, cronJob)
			if err != nil {
				log.Error(err, "failed to create CronJob")
				return ctrl.Result{}, err
			}
		} else {
			log.Error(err, "failed to get CronJob")
			return ctrl.Result{}, err
		}
	} else {
		log.Info("Skip reconcile: CronJob already exists", "CronJob.Namespace", found.Namespace, "CronJob.Name", found.Name)
		// Check if the following has changed: schedule, suspend, container image, PVC name, node name
		if found.Spec.Schedule != cronJob.Spec.Schedule ||
			(found.Spec.Suspend != nil && cronJob.Spec.Suspend != nil && *found.Spec.Suspend != *cronJob.Spec.Suspend) ||
			len(found.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 && len(cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 &&
				(found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image != cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image ||
					len(found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args) > 1 && len(cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args) > 1 &&
						found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args[1] != cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args[1]) ||
			found.Spec.JobTemplate.Spec.Template.Spec.NodeName != cronJob.Spec.JobTemplate.Spec.Template.Spec.NodeName {
			log.Info("Updating CronJob", "CronJob.Namespace", found.Namespace, "CronJob.Name", found.Name)
			found.Spec = cronJob.Spec
			err = r.Update(ctx, found)
			if err != nil {
				log.Error(err, "failed to update CronJob")
				return ctrl.Result{}, err
			}
		}
	}

	// Update status or other finalization
	return ctrl.Result{}, nil
}

func constructCronJob(backup *backupv1alpha1.KopiaBackup, nodeName string, appName string, repo *backupv1alpha1.KopiaRepository) *batchv1.CronJob {
	// Construct the CronJob based on the backup specification and nodeName
	// Implementation details here
	// Create a cronjob that runs echo and the pvc name
	// This is just an example, the actual implementation should be based on the Kopia backup tool
	// Keep only 1 successful backup history
	// Keep only 2 failed backup history
	// Mount the PVC to the pod under /data/<Namespace>/<appName (if exists)>/<PVCName>

	var mountPath string
	if appName != "" {
		mountPath = "/data/" + backup.Namespace + "/" + appName + "/" + backup.Spec.PVCName
	} else {
		mountPath = "/data/" + backup.Namespace + "/" + backup.Spec.PVCName
	}
	var kopiaCacheDirectory = filepath.Join(repo.Spec.FileSystemOptions.Path, ".kopia", "cache")
	var kopiaLogDir = filepath.Join(repo.Spec.FileSystemOptions.Path, ".kopia", "logs")

	var envVars = []corev1.EnvVar{
		{
			Name:  "KOPIA_CACHE_DIRECTORY",
			Value: kopiaCacheDirectory,
		},
		{
			Name:  "KOPIA_LOG_DIR",
			Value: kopiaLogDir,
		},
	}

	var envFrom = []corev1.EnvFromSource{}

	if repo.Spec.RepositoryPasswordExistingSecret != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: repo.Spec.RepositoryPasswordExistingSecret,
				},
			},
		})
	} else {
		if repo.Spec.RepositoryPassword != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KOPIA_PASSWORD",
				Value: repo.Spec.RepositoryPassword,
			})
		}
	}

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backup.Name + "-cronjob",
			Namespace: backup.Namespace,
		},
		Spec: batchv1.CronJobSpec{
			Schedule: backup.Spec.Schedule,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"backup.cloudinfra.be/pvc-name":  backup.Spec.PVCName,
								"backup.cloudinfra.be/node-name": nodeName,
								"app.kubernetes.io/name":         appName,
							},
						},
						Spec: corev1.PodSpec{
							NodeName: nodeName,
							Containers: []corev1.Container{
								{
									Name:  "snapshot",
									Image: "ghcr.io/fastlorenzo/kopia:0.16.1@sha256:e473aeb43e13e298853898c3613da2a4834f4bff2ccf747fbb2a90072d9e92c8",
									Args: []string{"/bin/bash", "-c", "" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[01/07] Create repo ...\"              && [[ ! -f " + repo.Spec.FileSystemOptions.Path + "/kopia.repository.f ]] && kopia repository create filesystem --path=" + repo.Spec.FileSystemOptions.Path + "\n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[02/07] Connect to repo ...\"          && kopia repo connect filesystem --path=" + repo.Spec.FileSystemOptions.Path + " --override-hostname=" + repo.Spec.Hostname + " --override-username=" + repo.Spec.Username + "\n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[03/07] Create snapshot ...\"          && kopia snap create " + mountPath + "\n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[04/07] List snapshots ...\"           && kopia snap list " + mountPath + "\n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[05/07] Show stats ...\"               && kopia content stats \n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[06/07] Show maintenance info ...\"      && kopia maintenance info \n" +
										"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[07/07] Disconnect repo ...\"           && kopia repo disconnect \n",
									},
									Env:     envVars,
									EnvFrom: envFrom,
									VolumeMounts: []corev1.VolumeMount{
										{
											Name:      "data",
											MountPath: mountPath,
										},
										{
											Name:      "repo",
											MountPath: repo.Spec.FileSystemOptions.Path,
										},
									},
								},
							},
							Volumes: []corev1.Volume{
								{
									Name: "data",
									VolumeSource: corev1.VolumeSource{
										PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
											ClaimName: backup.Spec.PVCName,
										},
									},
								},
								{
									Name: "repo",
									VolumeSource: corev1.VolumeSource{
										NFS: &corev1.NFSVolumeSource{
											Server: repo.Spec.FileSystemOptions.NFSServer,
											Path:   repo.Spec.FileSystemOptions.NFSPath,
										},
									},
								},
							},
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Tolerations: []corev1.Toleration{
								{
									Effect:   corev1.TaintEffectNoSchedule,
									Key:      "dedicated",
									Operator: corev1.TolerationOpExists,
								},
							},
						},
					},
					Suspend: &backup.Spec.Suspend,
				},
			},
		},
	}
	return cronJob
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaBackup{}).
		Owns(&batchv1.CronJob{}).
		Complete(r)
}
