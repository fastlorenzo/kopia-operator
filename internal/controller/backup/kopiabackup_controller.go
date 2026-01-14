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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
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

// KopiaBackupReconciler reconciles a KopiaBackup object
type KopiaBackupReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Log        logr.Logger
	RestConfig *rest.Config
	Recorder   record.EventRecorder
}

// Maximum number of backup history entries to keep
const maxBackupHistoryEntries = 3

//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=backup.cloudinfra.be,resources=kopiabackups/finalizers,verbs=update
//+kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	// Get the KopiaBackup instance
	var kBackup backupv1alpha1.KopiaBackup
	if err := r.Get(ctx, req.NamespacedName, &kBackup); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("KopiaBackup resource not found, will check if this is a PVC request")
			return handlePVCRequest(log, ctx, r, req)
		} else {
			log.Error(err, "unable to fetch KopiaBackup")
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	// Found the KopiaBackup instance

	// Update the Status
	kBackup.Status.Active = !kBackup.Spec.Suspend
	if err := r.Status().Update(ctx, &kBackup); err != nil {
		log.Error(err, "unable to update KopiaBackup status")
		return ctrl.Result{}, err
	}

	// Check if the PVC exists
	foundPVC, pvcErr := getRelatedPVC(log, ctx, r, &kBackup)
	// PVC might exist but we got an error while fetching it
	if pvcErr != nil {
		log.Error(pvcErr, "error getting related PVC")
		return ctrl.Result{}, pvcErr
	}
	// PVC does not exist
	if foundPVC == nil {
		log.Error(pvcErr, "PVC does not exist")
	}

	// Check if the KopiaBackup object should be deleted
	shouldDelete, deleteErr := shouldDeleteKopiaBackup(log, ctx, r, &kBackup, foundPVC)
	if shouldDelete {
		log.Info("KopiaBackup needs to be deleted")
		if deleteErr != nil {
			log.Error(deleteErr, "Error deleting KopiaBackup")
			return ctrl.Result{}, deleteErr
		}
		log.Info("KopiaBackup deleted")
		return ctrl.Result{}, nil
	}

	cronJobName := getCronJobNameFromPVCName(kBackup.Spec.PVCName)

	foundCronJob, shouldDeleteCronJob, cronJobRetrievalError :=
		getOrDeleteCronJob(log, ctx, r, cronJobName, &kBackup, foundPVC)

	if shouldDeleteCronJob {
		log.Info("CronJob needs to be deleted")
		if cronJobRetrievalError != nil {
			log.Error(cronJobRetrievalError, "Error deleting CronJob")
			return ctrl.Result{}, cronJobRetrievalError
		}
		log.Info("CronJob deleted")
		return ctrl.Result{}, nil
	}

	if cronJobRetrievalError != nil {
		log.Error(cronJobRetrievalError, "Error getting CronJob")
		return ctrl.Result{}, cronJobRetrievalError
	}

	// Check if the repository exists
	repository, repositoryErr := getKopiaRepositoryByName(ctx, r.Client, kBackup.Spec.Repository, log)
	if repositoryErr != nil {
		log.Error(repositoryErr, "error getting KopiaRepository", "repositoryName", kBackup.Spec.Repository)
		return ctrl.Result{}, repositoryErr
	}

	if repository == nil {
		log.Error(nil, "KopiaRepository is nil", "repositoryName", kBackup.Spec.Repository)
		return ctrl.Result{}, fmt.Errorf("KopiaRepository '%s' not found", kBackup.Spec.Repository)
	}

	log.Info("Found KopiaRepository", "repositoryName", repository.Name)

	// Handle server mode vs direct mode
	if repository.Spec.Server.Enabled {
		log.Info("Repository has server mode enabled, ensuring user credentials")

		// Create user manager
		userManager, err := NewKopiaUserManager(r.Client, r.Scheme, log, r.RestConfig)
		if err != nil {
			log.Error(err, "Failed to create user manager")
			return ctrl.Result{}, err
		}

		// Ensure user credentials for this backup
		_, err = userManager.EnsureUser(ctx, &kBackup, repository)
		if err != nil {
			// Check if the server is not ready yet
			var serverNotReady *ServerNotReadyError
			if errors.As(err, &serverNotReady) {
				log.Info("Kopia server not ready yet, will retry", "message", serverNotReady.Message)
				return ctrl.Result{RequeueAfter: time.Second * 10}, nil
			}
			log.Error(err, "failed to ensure user credentials for backup")
			return ctrl.Result{}, err
		}

		log.Info("User credentials ensured for server mode")
	} else {
		// Direct mode - handle configmap for filesystem storage
		if repository.Spec.StorageType == storageTypeFilesystem {
			configMap := &corev1.ConfigMap{}
			configMapName := fmt.Sprintf("kopia-config-%s", repository.Name)
			configMapRetrievalError := r.Get(ctx,
				types.NamespacedName{Name: configMapName, Namespace: kBackup.Namespace},
				configMap,
			)
			newConfigMap := constructConfigMap(&kBackup, repository)
			if configMapRetrievalError != nil {
				if client.IgnoreNotFound(configMapRetrievalError) != nil {
					// Real error, return
					log.Error(configMapRetrievalError, "unable to fetch ConfigMap")
					return ctrl.Result{}, configMapRetrievalError
				}
				// Not found, create the ConfigMap
				log.Info("ConfigMap not found, creating", "ConfigMap", configMapName)

				if err := r.Create(ctx, newConfigMap); err != nil {
					log.Error(err, "unable to create ConfigMap")
					return ctrl.Result{}, err
				}
			} else {
				log.Info("Found ConfigMap", "ConfigMap", configMap.Name)
				// Check if the ConfigMap has the correct configuration
				if shouldUpdateConfigMap(configMap, newConfigMap) {
					log.Info("Updating ConfigMap", "ConfigMap", configMap.Name)
					configMap.Data = newConfigMap.Data
					if err := r.Update(ctx, configMap); err != nil {
						log.Error(err, "unable to update ConfigMap")
						return ctrl.Result{}, err
					}
				}
			}
		}
	}

	// Get the runtime information (nodeName, appName, podName)
	nodeName, appName, podName, runtimeInfoErr := getRuntimeInfo(log, ctx, r, &kBackup)
	if runtimeInfoErr != nil {
		log.Error(runtimeInfoErr, "error getting runtime information")
		return ctrl.Result{}, runtimeInfoErr
	}

	// Add the pod name to the labels of the KopiaBackup object
	if podName != "" {
		if kBackup.Labels == nil {
			kBackup.Labels = make(map[string]string)
		}
		kBackup.Labels["backup.cloudinfra.be/pod-name"] = podName
		if err := r.Update(ctx, &kBackup); err != nil {
			log.Error(err, "unable to update KopiaBackup pod name label")
			return ctrl.Result{}, err
		}
	}

	// Re-queue the request if the pod is not found running
	if nodeName == "" {
		log.Info("No running pod found with the PVC mounted, requeuing")
		return ctrl.Result{Requeue: true}, nil
	}

	log.Info("Found node with running pod", "node", nodeName, "app", appName, "pod", podName)

	// Create or update the CronJob for the backup
	newCronJob := constructCronJob(&kBackup, cronJobName, nodeName, appName, repository)
	if newCronJob == nil {
		log.Error(nil, "constructCronJob returned nil")
		return ctrl.Result{}, fmt.Errorf("constructCronJob returned nil")
	}

	// Set the KopiaBackup object as the owner of the CronJob
	if err := ctrl.SetControllerReference(&kBackup, newCronJob, r.Scheme); err != nil {
		log.Error(err, "unable to set owner reference on cronJob")
		return ctrl.Result{}, err
	}

	// Logic to create or update the CronJob
	if foundCronJob == nil {
		log.Info("Creating a new CronJob",
			"CronJob.Namespace", newCronJob.Namespace,
			"CronJob.Name", newCronJob.Name,
		)
		err := r.Create(ctx, newCronJob)
		if err != nil {
			log.Error(err, "failed to create CronJob")
			return ctrl.Result{}, err
		}
	} else {
		log.Info("Check if the CronJob needs to be updated",
			"CronJob.Namespace", foundCronJob.Namespace,
			"CronJob.Name", foundCronJob.Name,
		)
		// Check if the CronJob needs to be updated
		if shouldUpdateCronJob(foundCronJob, newCronJob) {
			log.Info("Updating CronJob",
				"CronJob.Namespace", foundCronJob.Namespace,
				"CronJob.Name", foundCronJob.Name,
			)
			foundCronJob.Spec = newCronJob.Spec
			err := r.Update(ctx, foundCronJob)
			if err != nil {
				log.Error(err, "failed to update CronJob")
				return ctrl.Result{}, err
			}
		}
	}

	// Update backup status from related jobs
	if err := r.updateBackupStatusFromJobs(ctx, log, &kBackup); err != nil {
		log.Error(err, "failed to update backup status from jobs")
		// Don't return error, this is not critical
	}

	// Update status or other finalization
	return ctrl.Result{}, nil
}

func shouldUpdateCronJob(found *batchv1.CronJob, new *batchv1.CronJob) bool {

	// this function should return true if the cronjobs are different and need to be updated
	// else return false

	return !reflect.DeepEqual(found.Spec, new.Spec)
}

func shouldUpdateConfigMap(found *corev1.ConfigMap, new *corev1.ConfigMap) bool {
	// this function should return true if the configmaps are different and need to be updated
	// else return false

	return !reflect.DeepEqual(found.Data, new.Data)
}

// updateBackupStatusFromJobs updates the KopiaBackup status based on related Job executions
func (r *KopiaBackupReconciler) updateBackupStatusFromJobs(
	ctx context.Context,
	log logr.Logger,
	kBackup *backupv1alpha1.KopiaBackup,
) error {
	cronJobName := getCronJobNameFromPVCName(kBackup.Spec.PVCName)

	// List all jobs owned by the CronJob for this backup
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(kBackup.Namespace)); err != nil {
		return fmt.Errorf("failed to list jobs: %w", err)
	}

	// Filter jobs that belong to our CronJob (name starts with cronjob name)
	var relatedJobs []batchv1.Job
	for _, job := range jobList.Items {
		if strings.HasPrefix(job.Name, cronJobName+"-") {
			relatedJobs = append(relatedJobs, job)
		}
	}

	// Sort jobs by start time (newest first)
	sort.Slice(relatedJobs, func(i, j int) bool {
		iTime := relatedJobs[i].Status.StartTime
		jTime := relatedJobs[j].Status.StartTime
		if iTime == nil && jTime == nil {
			return false
		}
		if iTime == nil {
			return false
		}
		if jTime == nil {
			return true
		}
		return iTime.After(jTime.Time)
	})

	// Build backup history from jobs
	var backupHistory []backupv1alpha1.BackupHistoryEntry
	var lastBackupTime *metav1.Time
	var lastSuccessfulBackupTime *metav1.Time
	var lastBackupStatus backupv1alpha1.BackupStatus

	for i, job := range relatedJobs {
		entry := buildBackupHistoryEntry(&job)
		backupHistory = append(backupHistory, entry)

		// Set last backup info from the most recent job
		if i == 0 {
			lastBackupTime = job.Status.StartTime
			lastBackupStatus = entry.Status
		}

		// Track last successful backup
		if entry.Status == backupv1alpha1.BackupStatusSuccessful && lastSuccessfulBackupTime == nil {
			if job.Status.CompletionTime != nil {
				lastSuccessfulBackupTime = job.Status.CompletionTime
			} else if job.Status.StartTime != nil {
				lastSuccessfulBackupTime = job.Status.StartTime
			}
		}

		// Keep only the last N entries
		if len(backupHistory) >= maxBackupHistoryEntries {
			break
		}
	}

	// If no jobs exist yet, set status to Pending
	if len(relatedJobs) == 0 {
		lastBackupStatus = backupv1alpha1.BackupStatusPending
	}

	// Update status
	statusChanged := false

	if kBackup.Status.LastBackupStatus != lastBackupStatus {
		kBackup.Status.LastBackupStatus = lastBackupStatus
		statusChanged = true
	}

	if !reflect.DeepEqual(kBackup.Status.LastBackupTime, lastBackupTime) {
		kBackup.Status.LastBackupTime = lastBackupTime
		statusChanged = true
	}

	if !reflect.DeepEqual(kBackup.Status.LastSuccessfulBackupTime, lastSuccessfulBackupTime) {
		kBackup.Status.LastSuccessfulBackupTime = lastSuccessfulBackupTime
		statusChanged = true
	}

	if !reflect.DeepEqual(kBackup.Status.BackupHistory, backupHistory) {
		kBackup.Status.BackupHistory = backupHistory
		statusChanged = true
	}

	if statusChanged {
		if err := r.Status().Update(ctx, kBackup); err != nil {
			return fmt.Errorf("failed to update backup status: %w", err)
		}
		log.Info("Updated backup status from jobs",
			"lastBackupStatus", lastBackupStatus,
			"lastBackupTime", lastBackupTime,
			"lastSuccessfulBackupTime", lastSuccessfulBackupTime,
			"historyCount", len(backupHistory),
		)

		// Record event for status changes
		if lastBackupStatus == backupv1alpha1.BackupStatusSuccessful {
			r.Recorder.Event(kBackup, corev1.EventTypeNormal, "BackupSucceeded", "Backup completed successfully")
		} else if lastBackupStatus == backupv1alpha1.BackupStatusFailed {
			r.Recorder.Event(kBackup, corev1.EventTypeWarning, "BackupFailed", "Backup failed")
		}
	}

	return nil
}

// buildBackupHistoryEntry creates a BackupHistoryEntry from a Job
func buildBackupHistoryEntry(job *batchv1.Job) backupv1alpha1.BackupHistoryEntry {
	entry := backupv1alpha1.BackupHistoryEntry{
		JobName: job.Name,
	}

	// Set start time
	if job.Status.StartTime != nil {
		entry.StartTime = *job.Status.StartTime
	} else {
		entry.StartTime = job.CreationTimestamp
	}

	// Set completion time if available
	entry.CompletionTime = job.Status.CompletionTime

	// Determine status based on job conditions and status
	entry.Status = getJobBackupStatus(job)

	// Set message based on status
	switch entry.Status {
	case backupv1alpha1.BackupStatusSuccessful:
		entry.Message = fmt.Sprintf("Completed with %d succeeded pod(s)", job.Status.Succeeded)
	case backupv1alpha1.BackupStatusFailed:
		entry.Message = fmt.Sprintf("Failed with %d failed pod(s)", job.Status.Failed)
		// Try to get more details from conditions
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				if cond.Message != "" {
					entry.Message = cond.Message
				}
				break
			}
		}
	case backupv1alpha1.BackupStatusInProgress:
		entry.Message = "Backup is currently running"
	}

	return entry
}

// getJobBackupStatus determines the backup status from a Job's status
func getJobBackupStatus(job *batchv1.Job) backupv1alpha1.BackupStatus {
	// Check for completion
	for _, cond := range job.Status.Conditions {
		if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
			return backupv1alpha1.BackupStatusSuccessful
		}
		if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
			return backupv1alpha1.BackupStatusFailed
		}
	}

	// If not complete and not failed, it's in progress
	if job.Status.Active > 0 {
		return backupv1alpha1.BackupStatusInProgress
	}

	// If job was created but no pods started yet, consider it in progress
	if job.Status.Active == 0 && job.Status.Succeeded == 0 && job.Status.Failed == 0 {
		return backupv1alpha1.BackupStatusInProgress
	}

	// Fallback
	if job.Status.Succeeded > 0 {
		return backupv1alpha1.BackupStatusSuccessful
	}
	if job.Status.Failed > 0 {
		return backupv1alpha1.BackupStatusFailed
	}

	return backupv1alpha1.BackupStatusInProgress
}

func handlePVCRequest(
	log logr.Logger,
	ctx context.Context,
	r *KopiaBackupReconciler,
	req ctrl.Request,
) (ctrl.Result, error) {
	log.Info("Checking if this is a PVC request")
	// Check if the request is for a PVC
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, req.NamespacedName, pvc); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("PVC resource not found")
			return ctrl.Result{}, nil
		} else {
			log.Error(err, "unable to fetch PVC")
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	// Create a new KopiaBackup object for the PVC, if it has backup.cloudinfra.be/repository label set
	// and if backup.cloudinfra.be/repository is a valid KopiaRepository object
	if pvc.Labels == nil {
		log.Info("PVC does not have the required labels")
		return ctrl.Result{}, nil
	}

	repositoryName, ok := pvc.Labels["backup.cloudinfra.be/repository"]
	if !ok {
		log.Info("PVC does not have the required labels")
		return ctrl.Result{}, nil
	}

	log.Info("Checking if KopiaRepository exists", "KopiaRepository", repositoryName)

	// Check if the repository exists
	repository, repositoryErr := getKopiaRepositoryByName(ctx, r.Client, repositoryName, log)
	if repositoryErr != nil {
		log.Error(repositoryErr, "error getting KopiaRepository", "repositoryName", repositoryName)
		return ctrl.Result{}, repositoryErr
	}

	log.Info("Found KopiaRepository", "repositoryName", repository.Name)

	// Create a new KopiaBackup object
	newKopiaBackup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvc.Name,
			Namespace: pvc.Namespace,
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    pvc.Name,
			Repository: repository.Name,
			Schedule:   repository.Spec.DefaultSchedule,
		},
	}

	if err := ctrl.SetControllerReference(pvc, newKopiaBackup, r.Scheme); err != nil {
		log.Error(err, "unable to set owner reference on KopiaBackup")
		return ctrl.Result{}, err
	}

	if err := r.Create(ctx, newKopiaBackup); err != nil {
		log.Error(err, "unable to create KopiaBackup")
		return ctrl.Result{}, err
	}

	// Update the status
	newKopiaBackup.Status.Active = true
	newKopiaBackup.Status.FromAnnotation = true

	if err := r.Status().Update(ctx, newKopiaBackup); err != nil {
		log.Error(err, "unable to update KopiaBackup status")
		return ctrl.Result{}, err
	}

	log.Info("Created KopiaBackup", "KopiaBackup", newKopiaBackup.Name)
	return ctrl.Result{}, nil
}

func getRelatedPVC(
	log logr.Logger,
	ctx context.Context,
	r *KopiaBackupReconciler,
	kBackup *backupv1alpha1.KopiaBackup,
) (*corev1.PersistentVolumeClaim, error) {
	var pvcRetrievalError error
	if kBackup.Spec.PVCName != "" {
		pvcName := kBackup.Spec.PVCName
		foundPVC := &corev1.PersistentVolumeClaim{}
		pvcRetrievalError = r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: kBackup.Namespace}, foundPVC)
		if pvcRetrievalError != nil {
			if client.IgnoreNotFound(pvcRetrievalError) != nil {
				// Real error, return
				log.Error(pvcRetrievalError, "unable to fetch PVC")
				return nil, pvcRetrievalError
			}
			// Not found, continue
			log.Info("PVC not found", "PVC", pvcName)
			return nil, fmt.Errorf("PVC not found")
		}
		log.Info("Found PVC", "PVC", foundPVC.Name)
		return foundPVC, nil
	}

	// Throw an error if no PVC is specified
	log.Info("No PVC specified in KopiaBackup")
	return nil, fmt.Errorf("no PVC specified in KopiaBackup")
}

func shouldDeleteKopiaBackup(
	log logr.Logger,
	ctx context.Context,
	r *KopiaBackupReconciler,
	kBackup *backupv1alpha1.KopiaBackup,
	pvc *corev1.PersistentVolumeClaim,
) (bool, error) {
	// Check if the label backup.cloudinfra.be/repository is set on the PVC if kopiabackup.Status.FromAnnotation is true
	if kBackup.Status.FromAnnotation {
		log.Info("The KopiaBackup object was created from an annotation, checking if the PVC still has the required labels")
		_, ok := pvc.Labels["backup.cloudinfra.be/repository"]
		if !ok {
			log.Info("PVC does not have the required labels, deleting KopiaBackup")

			// Get the repository to check if server mode is enabled
			repository, err := getKopiaRepositoryByName(ctx, r.Client, kBackup.Spec.Repository, log)
			if err == nil && repository != nil && repository.Spec.Server.Enabled {
				// Server mode - cleanup user credentials
				log.Info("Cleaning up user credentials for server mode")
				userManager, err := NewKopiaUserManager(r.Client, r.Scheme, log, r.RestConfig)
				if err != nil {
					log.Error(err, "Failed to create user manager")
					return false, err
				}
				if err := userManager.DeleteUser(ctx, kBackup, repository); err != nil {
					log.Error(err, "failed to delete user credentials")
					// Continue with deletion even if cleanup fails
				}
			}

			// Delete the KopiaBackup object
			err = r.Delete(ctx, kBackup)
			if err != nil {
				log.Error(err, "unable to delete KopiaBackup")
				return true, err
			}
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

func getOrDeleteCronJob(
	log logr.Logger,
	ctx context.Context,
	r *KopiaBackupReconciler,
	cronJobName string,
	kBackup *backupv1alpha1.KopiaBackup,
	foundPVC *corev1.PersistentVolumeClaim,
) (*batchv1.CronJob, bool, error) {
	// Check if the CronJob exists
	cronJob := &batchv1.CronJob{}
	var cronJobRetrievalError error = r.Get(
		ctx,
		types.NamespacedName{Name: cronJobName, Namespace: kBackup.Namespace},
		cronJob,
	)
	if cronJobRetrievalError != nil {
		if client.IgnoreNotFound(cronJobRetrievalError) != nil {
			// Real error, return
			log.Error(cronJobRetrievalError, "unable to fetch CronJob")
			return nil, false, cronJobRetrievalError
		}
		// Not found, continue
		log.Info("CronJob not found", "CronJob", kBackup.Spec.Repository)
		return nil, false, nil
	}

	log.Info("Found CronJob", "CronJob", cronJob.Name)
	// Delete the CronJob if the PVC is not found
	if foundPVC == nil {
		log.Info("PVC not found, deleting CronJob", "CronJob", cronJob.Name)
		err := r.Delete(ctx, cronJob)
		if err != nil {
			log.Error(err, "unable to delete CronJob")
			return cronJob, true, err
		}
		// Return here to avoid further processing
		return cronJob, true, nil
	}

	return cronJob, false, nil
}

func getRuntimeInfo(
	log logr.Logger,
	ctx context.Context,
	r *KopiaBackupReconciler,
	kBackup *backupv1alpha1.KopiaBackup,
) (string, string, string, error) {
	// returns nodeName, appName, podName, error
	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(kBackup.Namespace)); err != nil {
		log.Error(err, "unable to list pods in the namespace")
		return "", "", "", err
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

	return nodeName, appName, podName, nil
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

	// If no KopiaBackup objects are linked, return a new reconcile.Request with the PVC name
	if len(requests) == 0 {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      pvc.GetName(),
				Namespace: pvc.GetNamespace(),
			},
		})
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

func (r *KopiaBackupReconciler) findBackupForJob(ctx context.Context, job client.Object) []reconcile.Request {
	// Find the KopiaBackup that owns the CronJob that created this Job
	// Job names from CronJobs follow the pattern: <cronjob-name>-<timestamp>
	// CronJob names follow the pattern: snapshot-<pvc-name>

	jobName := job.GetName()

	// Jobs created by our CronJobs start with "snapshot-"
	if !strings.HasPrefix(jobName, "snapshot-") {
		return []reconcile.Request{}
	}

	// List all KopiaBackups in the same namespace
	backupList := &backupv1alpha1.KopiaBackupList{}
	if err := r.List(ctx, backupList, client.InNamespace(job.GetNamespace())); err != nil {
		r.Log.Error(err, "unable to list KopiaBackups for job")
		return []reconcile.Request{}
	}

	var requests []reconcile.Request
	for _, backup := range backupList.Items {
		cronJobName := getCronJobNameFromPVCName(backup.Spec.PVCName)
		// Check if job name starts with the expected cronjob name
		if strings.HasPrefix(jobName, cronJobName+"-") {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      backup.GetName(),
					Namespace: backup.GetNamespace(),
				},
			})
			break // Each job belongs to only one CronJob/KopiaBackup
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

	// Add a runnable to reconcile all KopiaBackups on startup
	// This ensures that any changes in CronJob construction are applied to existing resources
	if err := mgr.Add(&kopiaBackupStartupReconciler{
		client: mgr.GetClient(),
		log:    r.Log,
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
		Watches(
			&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.findBackupForJob),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

// kopiaBackupStartupReconciler is a Runnable that triggers reconciliation of all KopiaBackups on startup
// This ensures that any changes in CronJob construction logic are applied to existing CronJobs
type kopiaBackupStartupReconciler struct {
	client client.Client
	log    logr.Logger
}

// Start implements the Runnable interface
func (r *kopiaBackupStartupReconciler) Start(ctx context.Context) error {
	// Wait a short time for the cache to sync
	time.Sleep(5 * time.Second)

	r.log.Info("Triggering startup reconciliation for all KopiaBackups")

	// List all KopiaBackups
	backupList := &backupv1alpha1.KopiaBackupList{}
	if err := r.client.List(ctx, backupList); err != nil {
		r.log.Error(err, "Failed to list KopiaBackups for startup reconciliation")
		return nil // Don't fail startup, just log the error
	}

	// Touch each KopiaBackup to trigger reconciliation by updating an annotation
	for _, backup := range backupList.Items {
		backupCopy := backup.DeepCopy()
		if backupCopy.Annotations == nil {
			backupCopy.Annotations = make(map[string]string)
		}
		backupCopy.Annotations["backup.cloudinfra.be/last-reconcile-trigger"] = time.Now().Format(time.RFC3339)

		if err := r.client.Update(ctx, backupCopy); err != nil {
			r.log.Error(err, "Failed to trigger reconciliation for KopiaBackup", "name", backup.Name, "namespace", backup.Namespace)
			continue
		}
		r.log.Info("Triggered reconciliation for KopiaBackup", "name", backup.Name, "namespace", backup.Namespace)
	}

	r.log.Info("Startup reconciliation trigger complete", "count", len(backupList.Items))
	return nil
}
