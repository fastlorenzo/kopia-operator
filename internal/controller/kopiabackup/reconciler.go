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

package kopiabackup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/kopia"
	kopiaMetrics "github.com/fastlorenzo/kopia-operator/internal/metrics"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

const (
	finalizerName = "backup.cloudinfra.be/finalizer"
	requeueDelay  = 30 * time.Second

	// pvcNameField is the field indexer for PVC name in KopiaBackup spec.
	pvcNameField = ".spec.pvcName"
	// repositoryNameField is the field indexer for repository name in KopiaBackup spec.
	repositoryNameField = ".spec.repository"

	// DefaultKopiaImage is the default Kopia container image used by backup CronJobs.
	DefaultKopiaImage = "ghcr.io/fastlorenzo/kopia:0.20.1@sha256:4a2660db62960eb0b4ba98982c4566bcc9dd2ee3b15b31af9626146aa4e5d8e3"
)

// KopiaBackupReconciler reconciles a KopiaBackup object.
type KopiaBackupReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// KopiaImage allows overriding the Kopia container image.
	// If empty, DefaultKopiaImage is used.
	KopiaImage string

	// ServerManager manages Kopia Server deployments (optional, for server mode).
	ServerManager kopia.ServerManager

	// UserManager manages Kopia Server users (optional, for server mode).
	UserManager kopia.UserManager
}

func (r *KopiaBackupReconciler) kopiaImage() string {
	if r.KopiaImage != "" {
		return r.KopiaImage
	}
	return DefaultKopiaImage
}

// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
// Secrets RBAC is cluster-wide because the operator creates per-backup user credential
// secrets with dynamic names. Kubebuilder markers don't support resourceNames restrictions
// on dynamically named resources, so we grant full Secret CRUD and rely on controller
// logic to only access secrets it owns.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiarepositories,verbs=get;list;watch

// Reconcile orchestrates the backup reconciliation through discrete phases.
func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	result, err := r.reconcile(ctx, req)
	if err != nil {
		kopiaMetrics.ReconcileErrors.WithLabelValues("kopiabackup").Inc()
	}
	return result, err
}

// reconcile contains the actual reconciliation logic.
func (r *KopiaBackupReconciler) reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	log := ctrllog.FromContext(ctx)

	var kBackup backupv1alpha1.KopiaBackup
	if err := r.Get(ctx, req.NamespacedName, &kBackup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Phase 1: Finalizer handling (no status updates needed)
	if done, err := r.handleFinalizer(ctx, &kBackup); done {
		return ctrl.Result{}, err
	}

	// Single deferred status update for all phases after finalizer handling.
	// Sub-methods set conditions in-memory; this persists them atomically.
	defer func() {
		if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
			if retErr == nil {
				retErr = statusErr
			}
		}
	}()

	// Phase 2: Validate PVC and repository
	repo, done := r.validateDependencies(ctx, &kBackup)
	if done {
		return ctrl.Result{}, nil
	}

	// Phase 3: Server mode credentials
	if repo.Spec.Server.Enabled {
		if result, done, err := r.reconcileServerCredentials(ctx, &kBackup, repo); done {
			return result, err
		}
	}

	// Phase 4: ConfigMap (direct mode, filesystem only)
	if !repo.Spec.Server.Enabled && repo.Spec.StorageType == backupv1alpha1.StorageTypeFilesystem {
		if err := r.reconcileConfigMap(ctx, &kBackup, repo); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile ConfigMap: %w", err)
		}
	}

	// Phase 5: Find pod, reconcile CronJob, finalize status
	return r.reconcileBackupResources(ctx, &kBackup, repo)
}

// --- Reconcile phases ---

// handleFinalizer manages finalizer addition and deletion cleanup.
// Returns done=true when reconciliation should stop.
func (r *KopiaBackupReconciler) handleFinalizer(ctx context.Context, backup *backupv1alpha1.KopiaBackup) (bool, error) {
	log := ctrllog.FromContext(ctx)

	if !backup.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(backup, finalizerName) {
			log.Info("Running finalizer cleanup")
			if r.UserManager != nil && backup.Spec.UserCredentialsSecret != "" {
				repo, err := r.getKopiaRepository(ctx, backup.Spec.Repository, backup.Namespace)
				if err == nil && repo.Spec.Server.Enabled {
					if delErr := r.UserManager.DeleteUser(ctx, backup, repo); delErr != nil {
						log.Error(delErr, "Failed to delete server user during finalization")
						r.Recorder.Event(backup, corev1.EventTypeWarning, "FinalizerFailed",
							fmt.Sprintf("Failed to delete server user: %v", delErr))
						return true, fmt.Errorf("finalizer cleanup failed, will retry: %w", delErr)
					}
				}
			}
			kopiaMetrics.CleanupBackupMetrics(backup.Name, backup.Namespace, backup.Spec.PVCName)
			controllerutil.RemoveFinalizer(backup, finalizerName)
			if err := r.Update(ctx, backup); err != nil {
				return true, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return true, nil
	}

	if !controllerutil.ContainsFinalizer(backup, finalizerName) {
		controllerutil.AddFinalizer(backup, finalizerName)
		if err := r.Update(ctx, backup); err != nil {
			return true, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	return false, nil
}

// validateDependencies validates the PVC and repository references.
// Returns the repository on success, or done=true with an appropriate result on failure.
// Does not requeue on validation errors — watches on PVCs and KopiaRepositories
// will trigger re-reconciliation when the missing dependency appears.
func (r *KopiaBackupReconciler) validateDependencies(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
) (*backupv1alpha1.KopiaRepository, bool) {

	if _, err := r.getRelatedPVC(ctx, backup); err != nil {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonPVCNotFound, fmt.Sprintf("PVC %q not found: %v", backup.Spec.PVCName, err))
		r.Recorder.Event(backup, corev1.EventTypeWarning, "PVCNotFound",
			fmt.Sprintf("PVC %q not found", backup.Spec.PVCName))
		return nil, true
	}

	repo, err := r.getKopiaRepository(ctx, backup.Spec.Repository, backup.Namespace)
	if err != nil {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonRepositoryNotFound, fmt.Sprintf("KopiaRepository %q not found", backup.Spec.Repository))
		r.Recorder.Event(backup, corev1.EventTypeWarning, "RepositoryNotFound",
			fmt.Sprintf("KopiaRepository %q not found", backup.Spec.Repository))
		return nil, true
	}

	return repo, false
}

// reconcileServerCredentials ensures user credentials exist for server mode.
// Returns done=true if the phase needs to stop reconciliation (error or requeue).
func (r *KopiaBackupReconciler) reconcileServerCredentials(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) (ctrl.Result, bool, error) {
	log := ctrllog.FromContext(ctx)
	if r.UserManager == nil {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonCronJobFailed, "Server mode requires UserManager to be configured")
		return ctrl.Result{}, true, fmt.Errorf("user manager not configured for server mode")
	}

	secretName, err := r.UserManager.EnsureUser(ctx, backup, repo)
	if err != nil {
		var serverNotReady *kopia.ServerNotReadyError
		if errors.As(err, &serverNotReady) {
			log.Info("Server not ready, requeuing", "error", err.Error())
			return ctrl.Result{RequeueAfter: requeueDelay}, true, nil
		}
		return ctrl.Result{}, true, fmt.Errorf("failed to ensure user: %w", err)
	}

	// Persist spec change (UserCredentialsSecret) to the API server.
	if backup.Spec.UserCredentialsSecret != secretName {
		backup.Spec.UserCredentialsSecret = secretName
		if err := r.Update(ctx, backup); err != nil {
			return ctrl.Result{}, true, fmt.Errorf("failed to persist UserCredentialsSecret: %w", err)
		}
	}

	// Status fields are persisted by the deferred Status().Update() in reconcile().
	backup.Status.ServerURL = r.ServerManager.GetServerURL(repo)
	backup.Status.Username = fmt.Sprintf("%s-%s@%s", backup.Namespace, backup.Spec.PVCName, repo.Spec.Hostname)
	backup.Status.Connected = true

	return ctrl.Result{}, false, nil
}

// reconcileBackupResources finds the pod using the PVC, reconciles the CronJob, and updates final status.
func (r *KopiaBackupReconciler) reconcileBackupResources(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) (ctrl.Result, error) {
	nodeName, appName, podName := r.findPodUsingPVC(ctx, backup)

	backup.Status.NodeName = nodeName

	cronJobName := naming.CronJobName(backup.Spec.PVCName)
	backup.Status.CronJobName = cronJobName

	// Always reconcile the CronJob so that schedule/config changes are applied
	// even when the consumer pod is temporarily absent.
	if err := r.reconcileCronJob(ctx, backup, cronJobName, nodeName, appName, repo); err != nil {
		r.setCondition(backup, backupv1alpha1.ConditionTypeCronJobCreated, metav1.ConditionFalse,
			backupv1alpha1.ReasonCronJobFailed, err.Error())
		r.Recorder.Event(backup, corev1.EventTypeWarning, "CronJobFailed", err.Error())
		return ctrl.Result{}, err
	}

	r.setCondition(backup, backupv1alpha1.ConditionTypeCronJobCreated, metav1.ConditionTrue,
		backupv1alpha1.ReasonReconciled, fmt.Sprintf("CronJob %q is up to date", cronJobName))

	if nodeName == "" {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonNoPodFound, "No running pod found with the PVC mounted")
		// Requeue: no watch on all pods; this is a genuinely transient condition.
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	if backup.Spec.Suspend {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonSuspended, "Backup is suspended")
	} else {
		r.setCondition(backup, backupv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
			backupv1alpha1.ReasonReconciled, fmt.Sprintf("Backup active on node %s (pod %s)", nodeName, podName))
	}

	return ctrl.Result{}, nil
}

// --- Helper methods ---

func (r *KopiaBackupReconciler) setCondition(backup *backupv1alpha1.KopiaBackup, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: backup.Generation,
	})
}

func (r *KopiaBackupReconciler) getRelatedPVC(ctx context.Context, kBackup *backupv1alpha1.KopiaBackup) (*corev1.PersistentVolumeClaim, error) {
	if kBackup.Spec.PVCName == "" {
		return nil, fmt.Errorf("spec.pvcName is required")
	}
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: kBackup.Spec.PVCName, Namespace: kBackup.Namespace}, &pvc); err != nil {
		return nil, fmt.Errorf("failed to get PVC %q: %w", kBackup.Spec.PVCName, err)
	}
	return &pvc, nil
}

// getKopiaRepository looks up a KopiaRepository by name, first in the given
// namespace and then across all namespaces. Returns an error if no match is
// found or if more than one cross-namespace match exists (ambiguous).
func (r *KopiaBackupReconciler) getKopiaRepository(ctx context.Context, name, namespace string) (*backupv1alpha1.KopiaRepository, error) {
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

func (r *KopiaBackupReconciler) findPodUsingPVC(ctx context.Context, kBackup *backupv1alpha1.KopiaBackup) (nodeName, appName, podName string) {
	log := ctrllog.FromContext(ctx)

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(kBackup.Namespace)); err != nil {
		log.Error(err, "Failed to list Pods")
		return "", "", ""
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if strings.HasPrefix(pod.Name, naming.CronJobPrefix) {
			continue
		}
		for _, volume := range pod.Spec.Volumes {
			if pvc := volume.PersistentVolumeClaim; pvc != nil && pvc.ClaimName == kBackup.Spec.PVCName {
				return pod.Spec.NodeName, pod.Labels["app.kubernetes.io/name"], pod.Name
			}
		}
	}
	return "", "", ""
}

// reconcileConfigMap creates or updates the Kopia config ConfigMap (direct mode only).
func (r *KopiaBackupReconciler) reconcileConfigMap(ctx context.Context, backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) error {
	configMapName := naming.ConfigMapName(repo.Name)
	desired, err := buildConfigMap(backup, repo)
	if err != nil {
		return fmt.Errorf("failed to build ConfigMap: %w", err)
	}

	if err := ctrl.SetControllerReference(backup, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on ConfigMap: %w", err)
	}

	existing := &corev1.ConfigMap{}
	getErr := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: backup.Namespace}, existing)
	if apierrors.IsNotFound(getErr) {
		return r.Create(ctx, desired)
	}
	if getErr != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", getErr)
	}

	if existing.Data["repository.config"] != desired.Data["repository.config"] {
		existing.Data = desired.Data
		return r.Update(ctx, existing)
	}
	return nil
}

// reconcileCronJob creates or updates the backup CronJob.
func (r *KopiaBackupReconciler) reconcileCronJob(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	cronJobName, nodeName, appName string,
	repo *backupv1alpha1.KopiaRepository,
) error {
	desired := buildCronJob(backup, cronJobName, nodeName, appName, repo, r.kopiaImage())
	if err := ctrl.SetControllerReference(backup, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference on CronJob: %w", err)
	}

	existing := &batchv1.CronJob{}
	err := r.Get(ctx, types.NamespacedName{Name: cronJobName, Namespace: backup.Namespace}, existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("failed to get CronJob: %w", err)
	}

	existing.Spec.Schedule = desired.Spec.Schedule
	existing.Spec.Suspend = desired.Spec.Suspend
	existing.Spec.ConcurrencyPolicy = desired.Spec.ConcurrencyPolicy
	existing.Spec.SuccessfulJobsHistoryLimit = desired.Spec.SuccessfulJobsHistoryLimit
	existing.Spec.FailedJobsHistoryLimit = desired.Spec.FailedJobsHistoryLimit
	existing.Spec.JobTemplate.Spec.Template.Spec = desired.Spec.JobTemplate.Spec.Template.Spec
	existing.Spec.JobTemplate.Spec.Suspend = desired.Spec.JobTemplate.Spec.Suspend

	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return nil
	}
	return r.Update(ctx, existing)
}

// --- Watch helpers ---

// findBackupsForField returns a handler.MapFunc that maps object changes to
// KopiaBackup reconcile requests using the given field index.
func (r *KopiaBackupReconciler) findBackupsForField(fieldName string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		log := ctrllog.FromContext(ctx)
		var backups backupv1alpha1.KopiaBackupList
		if err := r.List(ctx, &backups,
			client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{fieldName: obj.GetName()},
		); err != nil {
			log.Error(err, "Failed to list KopiaBackups", "field", fieldName, "value", obj.GetName(), "namespace", obj.GetNamespace())
			return nil
		}

		requests := make([]reconcile.Request, 0, len(backups.Items))
		for _, item := range backups.Items {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
			})
		}
		return requests
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Field indexer for PVC name lookup.
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(
		ctx,
		&backupv1alpha1.KopiaBackup{},
		pvcNameField,
		func(rawObj client.Object) []string {
			backup := rawObj.(*backupv1alpha1.KopiaBackup)
			if backup.Spec.PVCName == "" {
				return nil
			}
			return []string{backup.Spec.PVCName}
		},
	); err != nil {
		return err
	}

	// Field indexer for repository name lookup.
	if err := mgr.GetFieldIndexer().IndexField(
		ctx,
		&backupv1alpha1.KopiaBackup{},
		repositoryNameField,
		func(rawObj client.Object) []string {
			backup := rawObj.(*backupv1alpha1.KopiaBackup)
			if backup.Spec.Repository == "" {
				return nil
			}
			return []string{backup.Spec.Repository}
		},
	); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaBackup{}).
		Owns(&batchv1.CronJob{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(r.findBackupsForField(pvcNameField)),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Watches(
			&backupv1alpha1.KopiaRepository{},
			handler.EnqueueRequestsFromMapFunc(r.findBackupsForField(repositoryNameField)),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}
