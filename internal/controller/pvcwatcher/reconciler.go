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

package pvcwatcher

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

const (
	repositoryLabelKey    = "backup.cloudinfra.be/repository"
	scheduleAnnotationKey = "backup.cloudinfra.be/schedule"
)

// PVCWatcherReconciler watches PVCs with the backup repository label
// and automatically creates/deletes KopiaBackup resources.
type PVCWatcherReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *PVCWatcherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, req.NamespacedName, &pvc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	repoName := ""
	if pvc.Labels != nil {
		repoName = pvc.Labels[repositoryLabelKey]
	}

	// Check if an auto-created KopiaBackup exists for this PVC.
	var existingBackup backupv1alpha1.KopiaBackup
	backupKey := types.NamespacedName{Name: pvc.Name, Namespace: pvc.Namespace}
	backupExists := true
	if err := r.Get(ctx, backupKey, &existingBackup); err != nil {
		if apierrors.IsNotFound(err) {
			backupExists = false
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get KopiaBackup: %w", err)
		}
	}

	// Label removed → delete auto-created backup.
	if repoName == "" {
		if backupExists && (existingBackup.Status.AutoCreated || metav1.IsControlledBy(&existingBackup, &pvc)) {
			log.Info("PVC label removed, deleting auto-created KopiaBackup", "backup", existingBackup.Name)
			if err := r.Delete(ctx, &existingBackup); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed to delete KopiaBackup: %w", err)
			}
			r.Recorder.Event(&pvc, corev1.EventTypeNormal, "BackupDeleted",
				fmt.Sprintf("Deleted auto-created KopiaBackup %q (label removed)", existingBackup.Name))
		}
		return ctrl.Result{}, nil
	}

	// Label present + backup exists → sync schedule from PVC annotation.
	if backupExists {
		isAutoCreated := existingBackup.Status.AutoCreated || metav1.IsControlledBy(&existingBackup, &pvc)
		// Ensure AutoCreated status is set (may have been lost if status update failed after create).
		if isAutoCreated && !existingBackup.Status.AutoCreated {
			existingBackup.Status.AutoCreated = true
			if err := r.Status().Update(ctx, &existingBackup); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update KopiaBackup AutoCreated status: %w", err)
			}
		}
		if isAutoCreated {
			return r.syncSchedule(ctx, &pvc, &existingBackup, repoName)
		}
		return ctrl.Result{}, nil
	}

	// Label present + no backup → create KopiaBackup.
	return r.createBackupForPVC(ctx, &pvc, repoName)
}

// syncSchedule updates the KopiaBackup schedule from the PVC annotation.
func (r *PVCWatcherReconciler) syncSchedule(
	ctx context.Context,
	pvc *corev1.PersistentVolumeClaim,
	backup *backupv1alpha1.KopiaBackup,
	repoName string,
) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	repo, err := r.getKopiaRepository(ctx, repoName, pvc.Namespace)
	if err != nil {
		// Repo not found yet; will reconcile when the repo is created.
		return ctrl.Result{}, nil
	}

	newSchedule := getScheduleFromPVC(pvc, repo.Spec.DefaultSchedule)
	if newSchedule != backup.Spec.Schedule {
		log.Info("Updating schedule from PVC annotation", "old", backup.Spec.Schedule, "new", newSchedule)
		backup.Spec.Schedule = newSchedule
		if err := r.Update(ctx, backup); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update schedule: %w", err)
		}
	}
	return ctrl.Result{}, nil
}

// createBackupForPVC creates a new KopiaBackup owned by the given PVC.
func (r *PVCWatcherReconciler) createBackupForPVC(
	ctx context.Context,
	pvc *corev1.PersistentVolumeClaim,
	repoName string,
) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	repo, err := r.getKopiaRepository(ctx, repoName, pvc.Namespace)
	if err != nil {
		log.Info("KopiaRepository not found for PVC", "repository", repoName)
		return ctrl.Result{}, nil
	}

	newBackup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvc.Name,
			Namespace: pvc.Namespace,
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    pvc.Name,
			Repository: repoName,
			Schedule:   getScheduleFromPVC(pvc, repo.Spec.DefaultSchedule),
		},
	}

	if err := ctrl.SetControllerReference(pvc, newBackup, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference: %w", err)
	}

	if err := r.Create(ctx, newBackup); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to create KopiaBackup: %w", err)
	}

	newBackup.Status.AutoCreated = true
	if err := r.Status().Update(ctx, newBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update KopiaBackup status: %w", err)
	}

	r.Recorder.Event(newBackup, corev1.EventTypeNormal, "Created",
		fmt.Sprintf("Auto-created KopiaBackup for PVC %s", pvc.Name))
	log.Info("Created KopiaBackup from PVC label", "backup", newBackup.Name)
	return ctrl.Result{}, nil
}

// getKopiaRepository looks up a KopiaRepository by name, first in the given
// namespace and then across all namespaces. Returns an error if no match is
// found or if more than one cross-namespace match exists (ambiguous).
func (r *PVCWatcherReconciler) getKopiaRepository(ctx context.Context, name, namespace string) (*backupv1alpha1.KopiaRepository, error) {
	// Try the local namespace first.
	var repo backupv1alpha1.KopiaRepository
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &repo); err == nil {
		return &repo, nil
	}

	// Fall back to searching all namespaces.
	var repoList backupv1alpha1.KopiaRepositoryList
	if err := r.List(ctx, &repoList); err != nil {
		return nil, fmt.Errorf("listing KopiaRepositories: %w", err)
	}

	var matches []backupv1alpha1.KopiaRepository
	for i := range repoList.Items {
		if repoList.Items[i].Name == name {
			matches = append(matches, repoList.Items[i])
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("KopiaRepository %q not found in namespace %q or cluster-wide", name, namespace)
	case 1:
		return &matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous: found %d KopiaRepositories named %q across namespaces", len(matches), name)
	}
}

// getScheduleFromPVC reads the backup schedule annotation from a PVC,
// falling back to defaultSchedule when the annotation is absent.
func getScheduleFromPVC(pvc *corev1.PersistentVolumeClaim, defaultSchedule string) string {
	if pvc != nil && pvc.Annotations != nil {
		if schedule, ok := pvc.Annotations[scheduleAnnotationKey]; ok && schedule != "" {
			return schedule
		}
	}
	return defaultSchedule
}

// SetupWithManager sets up the controller with the Manager.
func (r *PVCWatcherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("pvc-watcher").
		For(&corev1.PersistentVolumeClaim{}).
		WithEventFilter(predicate.Or(
			predicate.LabelChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
		)).
		Complete(r)
}
