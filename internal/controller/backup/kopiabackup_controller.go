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

package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/go-logr/logr"
)

const (
	pvcNameField = ".spec.pvcName"
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
	log := ctrllog.FromContext(ctx)

	// Get the KopiaBackup instance
	var kBackup backupv1alpha1.KopiaBackup
	if err := r.Get(ctx, req.NamespacedName, &kBackup); err != nil {
		log.Error(err, "unable to fetch KopiaBackup")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Get the referenced PVC
	// var pvcVersion string
	var pvcRetrievalError error
	if kBackup.Spec.PVCName != "" {
		pvcName := kBackup.Spec.PVCName
		foundPVC := &corev1.PersistentVolumeClaim{}
		pvcRetrievalError = r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: kBackup.Namespace}, foundPVC)
		if pvcRetrievalError != nil {
			if client.IgnoreNotFound(pvcRetrievalError) != nil {
				// Real error, return
				log.Error(pvcRetrievalError, "unable to fetch PVC")
				return ctrl.Result{}, pvcRetrievalError
			}
			// Not found, continue
			log.Info("PVC not found", "PVC", pvcName)
		} else {
			log.Info("Found PVC", "PVC", foundPVC.Name)
		}
		// pvcVersion = foundPVC.ResourceVersion
	} else {
		// Throw an error if no PVC is specified
		log.Info("No PVC specified in KopiaBackup")
		return ctrl.Result{}, fmt.Errorf("no PVC specified in KopiaBackup")
	}

	// Generate the name of the cronjob based on the following: name: "{{ regex_replace_all('^([a-z0-9-]{0,42}).*([a-z0-9])$', '{{claimName}}', 'snapshot-$${1}$${2}') }}"
	// The cronjob name should be snapshot-<first 42 characters of the PVC name>-<last character of the PVC name>
	var cronJobName string
	if len(kBackup.Spec.PVCName) > 42 {
		cronJobName = "snapshot-" + kBackup.Spec.PVCName[:42] + "-" + string(kBackup.Spec.PVCName[len(kBackup.Spec.PVCName)-1])
	} else {
		cronJobName = "snapshot-" + kBackup.Spec.PVCName
	}

	// Check if the CronJob exists
	cronJob := &batchv1.CronJob{}
	var cronJobRetrievalError error
	cronJobRetrievalError = r.Get(ctx, types.NamespacedName{Name: cronJobName, Namespace: kBackup.Namespace}, cronJob)
	if cronJobRetrievalError != nil {
		if client.IgnoreNotFound(cronJobRetrievalError) != nil {
			// Real error, return
			log.Error(cronJobRetrievalError, "unable to fetch CronJob")
			return ctrl.Result{}, cronJobRetrievalError
		}
		// Not found, continue
		log.Info("CronJob not found", "CronJob", kBackup.Spec.Repository)
	} else {
		log.Info("Found CronJob", "CronJob", cronJob.Name)
		// Delete the CronJob if the PVC is not found
		if pvcRetrievalError != nil && client.IgnoreNotFound(pvcRetrievalError) == nil {
			log.Info("PVC not found, deleting CronJob", "CronJob", cronJob.Name)
			err := r.Delete(ctx, cronJob)
			if err != nil {
				log.Error(err, "unable to delete CronJob")
				return ctrl.Result{}, err
			}
			// Return here to avoid further processing
			return ctrl.Result{}, nil
		}
	}

	// Check if the repository exists (KopiaRepository) - name is in backup.Spec.Repository
	repository := &backupv1alpha1.KopiaRepository{}
	err := r.Get(ctx, types.NamespacedName{Name: kBackup.Spec.Repository, Namespace: kBackup.Namespace}, repository)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			// Real error, return
			log.Error(err, "unable to fetch KopiaRepository")
			return ctrl.Result{}, err
		}
		// Not found, return
		log.Error(err, "Referenced KopiaRepository not found", "KopiaRepository", kBackup.Spec.Repository)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Found KopiaRepository", "KopiaRepository", repository.Name)

	// List all Pods in the same namespace
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(kBackup.Namespace)); err != nil {
		log.Error(err, "unable to list pods in the namespace")
		return ctrl.Result{}, err
	}

	var nodeName string
	var appName string // label app.kubernetes.io/name
	var podName string
	for _, pod := range podList.Items {
		// Check if pod is running and has the PVC mounted
		if pod.Status.Phase == corev1.PodRunning {
			for _, volume := range pod.Spec.Volumes {
				if pvc := volume.PersistentVolumeClaim; pvc != nil && pvc.ClaimName == kBackup.Spec.PVCName {
					// Skip backup pods where name starts with snapshot-
					if strings.HasPrefix(pod.Name, "snapshot-") {
						continue
					}

					nodeName = pod.Spec.NodeName
					// Check if the pod has the label app.kubernetes.io/name
					appName = pod.Labels["app.kubernetes.io/name"]
					podName = pod.Name
					break
				}
			}
		}
		if nodeName != "" {
			break
		}
	}
	if podName != "" {
		// Add the pod name to the labels of the KopiaBackup object
		if kBackup.Labels == nil {
			kBackup.Labels = make(map[string]string)
		}
		kBackup.Labels["backup.cloudinfra.be/pod-name"] = podName
		if err := r.Update(ctx, &kBackup); err != nil {
			log.Error(err, "unable to update KopiaBackup pod name label")
			return ctrl.Result{}, err
		}
	}
	if nodeName == "" {
		log.Info("No running pod found with the PVC mounted, requeuing")
		return ctrl.Result{Requeue: true}, nil
	}

	log.Info("Found node with running pod", "node", nodeName)

	// Create or update the CronJob for the backup
	newCronJob := constructCronJob(&kBackup, cronJobName, nodeName, appName, repository)
	if newCronJob == nil {
		log.Error(nil, "constructCronJob returned nil")
		return ctrl.Result{}, fmt.Errorf("constructCronJob returned nil")
	}

	if err := ctrl.SetControllerReference(&kBackup, newCronJob, r.Scheme); err != nil {
		log.Error(err, "unable to set owner reference on cronJob")
		return ctrl.Result{}, err
	}

	// Logic to create or update the CronJob
	found := &batchv1.CronJob{}
	err = r.Get(ctx, types.NamespacedName{Name: newCronJob.Name, Namespace: newCronJob.Namespace}, found)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Creating a new CronJob", "CronJob.Namespace", newCronJob.Namespace, "CronJob.Name", newCronJob.Name)
			err = r.Create(ctx, newCronJob)
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
		if found.Spec.Schedule != newCronJob.Spec.Schedule ||
			(found.Spec.Suspend != nil && newCronJob.Spec.Suspend != nil && *found.Spec.Suspend != *newCronJob.Spec.Suspend) ||
			len(found.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 && len(newCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers) > 0 &&
				(found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image != newCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image ||
					len(found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args) > 1 && len(newCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args) > 1 &&
						found.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args[1] != newCronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Args[1]) ||
			found.Spec.JobTemplate.Spec.Template.Spec.NodeName != newCronJob.Spec.JobTemplate.Spec.Template.Spec.NodeName {
			log.Info("Updating CronJob", "CronJob.Namespace", found.Namespace, "CronJob.Name", found.Name)
			found.Spec = newCronJob.Spec
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

func constructCronJob(backup *backupv1alpha1.KopiaBackup, cronJobName string, nodeName string, appName string, repo *backupv1alpha1.KopiaRepository) *batchv1.CronJob {
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
			Name:      cronJobName,
			Namespace: backup.Namespace,
		},
		Spec: batchv1.CronJobSpec{
			ConcurrencyPolicy: batchv1.ForbidConcurrent,
			Schedule:          backup.Spec.Schedule,
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

func (r *KopiaBackupReconciler) findObjectsForPVC(ctx context.Context, pvc client.Object) []reconcile.Request {
	// Find all KopiaBackup objects that reference this PVC
	attachedKopiaBackups := &backupv1alpha1.KopiaBackupList{}
	listOps := &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(pvcNameField, pvc.GetName()),
		Namespace:     pvc.GetNamespace(),
	}
	err := r.List(ctx, attachedKopiaBackups, listOps)
	if err != nil {
		r.Log.Error(err, "unable to list KopiaBackups")
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, len(attachedKopiaBackups.Items))
	for i, item := range attachedKopiaBackups.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			},
		}
	}

	return requests
}

func (r *KopiaBackupReconciler) findObjectsForPod(ctx context.Context, pod client.Object) []reconcile.Request {
	// Find all KopiaBackup objects that are linked to this pod
	attachedKopiaBackups := &backupv1alpha1.KopiaBackupList{}
	listOps := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(labels.Set{"backup.cloudinfra.be/pod-name": pod.GetName()}),
		Namespace:     pod.GetNamespace(),
	}
	err := r.List(ctx, attachedKopiaBackups, listOps)
	if err != nil {
		r.Log.Error(err, "unable to list KopiaBackups")
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, len(attachedKopiaBackups.Items))
	for i, item := range attachedKopiaBackups.Items {
		requests[i] = reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      item.GetName(),
				Namespace: item.GetNamespace(),
			},
		}
	}

	return requests
}

func (r *KopiaBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &backupv1alpha1.KopiaBackup{}, pvcNameField, func(rawObj client.Object) []string {
		// Extract the PVC Name from the KopiaBackup object, if it is set
		kBackup := rawObj.(*backupv1alpha1.KopiaBackup)
		if kBackup.Spec.PVCName == "" {
			return nil
		}

		return []string{kBackup.Spec.PVCName}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaBackup{}).
		Owns(&batchv1.CronJob{}).
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForPVC),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForPod),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}
