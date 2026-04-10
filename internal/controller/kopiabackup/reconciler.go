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
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

const (
	scheduleAnnotationKey = "backup.cloudinfra.be/schedule"
	repositoryLabelKey    = "backup.cloudinfra.be/repository"
	finalizerName         = "backup.cloudinfra.be/finalizer"
	requeueDelay          = 30 * time.Second

	// pvcNameField is the field indexer for PVC name in KopiaBackup spec.
	pvcNameField = ".spec.pvcName"

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
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	// --- Fetch the KopiaBackup ---
	var kBackup backupv1alpha1.KopiaBackup
	if err := r.Get(ctx, req.NamespacedName, &kBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return r.handlePVCRequest(ctx, req)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get KopiaBackup: %w", err)
	}

	// --- Finalizer handling ---
	if !kBackup.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&kBackup, finalizerName) {
			log.Info("Running finalizer cleanup")
			if r.UserManager != nil && kBackup.Spec.UserCredentialsSecret != "" {
				repo, err := r.getKopiaRepository(ctx, kBackup.Spec.Repository, kBackup.Namespace)
				if err == nil && repo.Spec.Server.Enabled {
					if delErr := r.UserManager.DeleteUser(ctx, &kBackup, repo); delErr != nil {
						log.Error(delErr, "Failed to delete server user during finalization")
					}
				}
			}
			controllerutil.RemoveFinalizer(&kBackup, finalizerName)
			if err := r.Update(ctx, &kBackup); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&kBackup, finalizerName) {
		controllerutil.AddFinalizer(&kBackup, finalizerName)
		if err := r.Update(ctx, &kBackup); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	// --- Validate PVC ---
	foundPVC, err := r.getRelatedPVC(ctx, &kBackup)
	if err != nil {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonPVCNotFound, fmt.Sprintf("PVC %q not found: %v", kBackup.Spec.PVCName, err))
		if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		r.Recorder.Event(&kBackup, corev1.EventTypeWarning, "PVCNotFound",
			fmt.Sprintf("PVC %q not found", kBackup.Spec.PVCName))
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Auto-delete KopiaBackup when the PVC label is removed (annotation-created only)
	if kBackup.Status.FromAnnotation {
		if foundPVC.Labels == nil || foundPVC.Labels[repositoryLabelKey] == "" {
			log.Info("PVC label removed, deleting auto-created KopiaBackup")
			if err := r.Delete(ctx, &kBackup); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete KopiaBackup: %w", err)
			}
			return ctrl.Result{}, nil
		}
	}

	// --- Validate repository ---
	repository, err := r.getKopiaRepository(ctx, kBackup.Spec.Repository, kBackup.Namespace)
	if err != nil {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonRepositoryNotFound, fmt.Sprintf("KopiaRepository %q not found", kBackup.Spec.Repository))
		if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		r.Recorder.Event(&kBackup, corev1.EventTypeWarning, "RepositoryNotFound",
			fmt.Sprintf("KopiaRepository %q not found in namespace %q", kBackup.Spec.Repository, kBackup.Namespace))
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	// Sync schedule from PVC annotation for auto-created backups
	if kBackup.Status.FromAnnotation {
		newSchedule := getScheduleFromPVC(foundPVC, repository.Spec.DefaultSchedule)
		if newSchedule != kBackup.Spec.Schedule {
			log.Info("Updating schedule from PVC annotation", "old", kBackup.Spec.Schedule, "new", newSchedule)
			kBackup.Spec.Schedule = newSchedule
			if err := r.Update(ctx, &kBackup); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update schedule: %w", err)
			}
		}
	}

	// --- Server mode: ensure user credentials ---
	if repository.Spec.Server.Enabled {
		if r.UserManager == nil {
			r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
				backupv1alpha1.ReasonCronJobFailed, "Server mode requires UserManager to be configured")
			if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
				log.Error(statusErr, "Failed to update status")
			}
			return ctrl.Result{}, fmt.Errorf("UserManager not configured for server mode")
		}

		secretName, err := r.UserManager.EnsureUser(ctx, &kBackup, repository)
		if err != nil {
			var serverNotReady *kopia.ServerNotReadyError
			if errors.As(err, &serverNotReady) {
				log.Info("Server not ready, requeuing", "error", err.Error())
				return ctrl.Result{RequeueAfter: requeueDelay}, nil
			}
			return ctrl.Result{}, fmt.Errorf("failed to ensure user: %w", err)
		}

		kBackup.Spec.UserCredentialsSecret = secretName
		kBackup.Status.ServerURL = r.ServerManager.GetServerURL(repository)
		kBackup.Status.Username = fmt.Sprintf("%s-%s@%s", kBackup.Namespace, kBackup.Spec.PVCName, repository.Spec.Hostname)
		kBackup.Status.Connected = true
	}

	// --- Manage ConfigMap (direct mode, filesystem only) ---
	if !repository.Spec.Server.Enabled && repository.Spec.StorageType == backupv1alpha1.StorageTypeFilesystem {
		if err := r.reconcileConfigMap(ctx, &kBackup, repository); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to reconcile ConfigMap: %w", err)
		}
	}

	// --- Find the running pod that mounts the PVC ---
	nodeName, appName, podName := r.findPodUsingPVC(ctx, &kBackup)
	if nodeName == "" {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonNoPodFound, "No running pod found with the PVC mounted")
		if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		return ctrl.Result{RequeueAfter: requeueDelay}, nil
	}

	kBackup.Status.NodeName = nodeName

	// --- Reconcile the CronJob ---
	cronJobName := naming.CronJobName(kBackup.Spec.PVCName)
	kBackup.Status.CronJobName = cronJobName

	if err := r.reconcileCronJob(ctx, &kBackup, cronJobName, nodeName, appName, repository); err != nil {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeCronJobCreated, metav1.ConditionFalse,
			backupv1alpha1.ReasonCronJobFailed, err.Error())
		if statusErr := r.Status().Update(ctx, &kBackup); statusErr != nil {
			log.Error(statusErr, "Failed to update status")
		}
		r.Recorder.Event(&kBackup, corev1.EventTypeWarning, "CronJobFailed", err.Error())
		return ctrl.Result{}, err
	}

	r.setCondition(&kBackup, backupv1alpha1.ConditionTypeCronJobCreated, metav1.ConditionTrue,
		backupv1alpha1.ReasonReconciled, fmt.Sprintf("CronJob %q is up to date", cronJobName))

	// --- Mark ready ---
	if kBackup.Spec.Suspend {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionFalse,
			backupv1alpha1.ReasonSuspended, "Backup is suspended")
	} else {
		r.setCondition(&kBackup, backupv1alpha1.ConditionTypeReady, metav1.ConditionTrue,
			backupv1alpha1.ReasonReconciled, fmt.Sprintf("Backup active on node %s (pod %s)", nodeName, podName))
	}
	if err := r.Status().Update(ctx, &kBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
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

func (r *KopiaBackupReconciler) handlePVCRequest(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, req.NamespacedName, &pvc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if pvc.Labels == nil {
		return ctrl.Result{}, nil
	}
	repositoryName, ok := pvc.Labels[repositoryLabelKey]
	if !ok || repositoryName == "" {
		return ctrl.Result{}, nil
	}

	repository, err := r.getKopiaRepository(ctx, repositoryName, pvc.Namespace)
	if err != nil {
		log.Info("KopiaRepository not found for PVC", "repository", repositoryName)
		return ctrl.Result{}, nil
	}

	newBackup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvc.Name,
			Namespace: pvc.Namespace,
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    pvc.Name,
			Repository: repositoryName,
			Schedule:   getScheduleFromPVC(&pvc, repository.Spec.DefaultSchedule),
		},
	}

	if err := ctrl.SetControllerReference(&pvc, newBackup, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference: %w", err)
	}

	if err := r.Create(ctx, newBackup); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to create KopiaBackup: %w", err)
	}

	newBackup.Status.FromAnnotation = true
	if err := r.Status().Update(ctx, newBackup); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update KopiaBackup status: %w", err)
	}

	r.Recorder.Event(newBackup, corev1.EventTypeNormal, "Created",
		fmt.Sprintf("Auto-created KopiaBackup for PVC %s", pvc.Name))
	log.Info("Created KopiaBackup from PVC label", "backup", newBackup.Name)
	return ctrl.Result{}, nil
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

func (r *KopiaBackupReconciler) getKopiaRepository(ctx context.Context, name, namespace string) (*backupv1alpha1.KopiaRepository, error) {
	var repo backupv1alpha1.KopiaRepository
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &repo); err != nil {
		return nil, fmt.Errorf("failed to get KopiaRepository %q: %w", name, err)
	}
	return &repo, nil
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
		if strings.HasPrefix(pod.Name, "snapshot-") {
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
	existing.Spec.JobTemplate.Spec.Template.Spec = desired.Spec.JobTemplate.Spec.Template.Spec
	existing.Spec.JobTemplate.Spec.Suspend = desired.Spec.JobTemplate.Spec.Suspend
	return r.Update(ctx, existing)
}

// --- Pure functions ---

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

// --- Watch helpers ---

func (r *KopiaBackupReconciler) findObjectsForPVC(ctx context.Context, pvc client.Object) []reconcile.Request {
	var backups backupv1alpha1.KopiaBackupList
	if err := r.List(ctx, &backups,
		client.InNamespace(pvc.GetNamespace()),
		client.MatchingFields{pvcNameField: pvc.GetName()},
	); err != nil {
		return nil
	}

	requests := make([]reconcile.Request, 0, len(backups.Items)+1)
	for _, item := range backups.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: item.Name, Namespace: item.Namespace},
		})
	}

	// If no existing backup references this PVC, enqueue the PVC name so handlePVCRequest can fire
	if len(requests) == 0 {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: pvc.GetName(), Namespace: pvc.GetNamespace()},
		})
	}

	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *KopiaBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&backupv1alpha1.KopiaBackup{}).
		Owns(&batchv1.CronJob{}).
		Watches(
			&corev1.PersistentVolumeClaim{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForPVC),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}
