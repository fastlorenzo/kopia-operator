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

	batchv1beta1 "k8s.io/api/batch/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

	// TODO(user): your logic here
	// Fetch the KopiaBackup instance
	backup := &backupv1alpha1.KopiaBackup{}
	err := r.Get(ctx, req.NamespacedName, backup)
	if err != nil {
		log.Error(err, "unable to fetch KopiaBackup")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// List all Pods in the same namespace
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(backup.Namespace)); err != nil {
		log.Error(err, "unable to list pods in the namespace")
		return ctrl.Result{}, err
	}

	var nodeName string
	for _, pod := range podList.Items {
		// Check if pod is running and has the PVC mounted
		if pod.Status.Phase == corev1.PodRunning {
			for _, volume := range pod.Spec.Volumes {
				if pvc := volume.PersistentVolumeClaim; pvc != nil && pvc.ClaimName == backup.Spec.PVCName {
					nodeName = pod.Spec.NodeName
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
	cronJob := constructCronJob(backup, nodeName)
	if err := ctrl.SetControllerReference(backup, cronJob, r.Scheme); err != nil {
		log.Error(err, "unable to set owner reference on cronJob")
		return ctrl.Result{}, err
	}

	// Logic to create or update the CronJob
	found := &batchv1beta1.CronJob{}
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
		// Update existing CronJob if necessary
		// Update logic here if needed
	}

	// Update status or other finalization
	return ctrl.Result{}, nil
}

func constructCronJob(backup *backupv1alpha1.KopiaBackup, nodeName string) *batchv1beta1.CronJob {
	// Construct the CronJob based on the backup specification and nodeName
	// Implementation details here
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaBackup{}).
		Owns(&batchv1beta1.CronJob{}).
		Complete(r)
}
