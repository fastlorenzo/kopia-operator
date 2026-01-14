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
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
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
// TODO(user): Modify the Reconcile function to compare the state specified by
// the KopiaBackup object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrllog.FromContext(ctx)

	log.Info("Received reconcile request", "Name", req.Name, "Namespace", req.Namespace, "Request", req, "Context", ctx)

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

func getCronJobNameFromPVCName(pvcName string) string {
	// Generate the name of the cronjob based on the following:
	// name: "{{ regex_replace_all('^([a-z0-9-]{0,42}).*([a-z0-9])$', '{{claimName}}', 'snapshot-$${1}$${2}') }}"
	// The cronjob name should be snapshot-<first 42 characters of the PVC name>-<last character of the PVC name>

	if len(pvcName) > 42 {
		return "snapshot-" + pvcName[:42] + "-" + string(pvcName[len(pvcName)-1])
	}

	return "snapshot-" + pvcName
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

// buildBackupCommand builds the command to run in the backup container
func buildBackupCommand(backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository, mountPath string) string {
	if repo.Spec.Server.Enabled {
		// Server mode - connect to Kopia server instead of direct repository
		// Kopia requires HTTPS for server connections
		serverURL := fmt.Sprintf("https://kopia-server-%s.%s.svc.cluster.local:51515",
			repo.Name,
			repo.Namespace)

		// Credentials are provided via environment variables from the secret
		// KOPIA_SERVER_USERNAME format is "username@hostname", we extract parts using shell parameter expansion
		// KOPIA_PASSWORD is set from KOPIA_SERVER_PASSWORD via env var in the container spec
		// KOPIA_TLS_FINGERPRINT is the server's TLS certificate fingerprint from the TLS secret
		return "" +
			"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[01/04] Connecting to Kopia Server...\"    && " +
			"kopia repository connect server --url=" + serverURL + " " +
			"--server-cert-fingerprint=\"${KOPIA_TLS_FINGERPRINT}\" " +
			"--override-username=\"${KOPIA_SERVER_USERNAME%%@*}\" " +
			"--override-hostname=\"${KOPIA_SERVER_USERNAME#*@}\" && " +
			"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[02/04] Create snapshot ...\"          && kopia snap create " + mountPath + " && " +
			"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[03/04] List snapshots ...\"           && kopia snap list " + mountPath + " && " +
			"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[04/04] Disconnect repo ...\"           && kopia repo disconnect \n"
	}

	// Direct mode - original behavior
	return "" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[01/04] Create snapshot ...\"          && kopia snap create " + mountPath + "\n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[02/04] List snapshots ...\"           && kopia snap list " + mountPath + "\n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[03/04] Show stats ...\"               && kopia content stats \n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[04/04] Show maintenance info ...\"      && kopia maintenance info \n"
}

func constructCronJob(
	backup *backupv1alpha1.KopiaBackup,
	cronJobName string,
	nodeName string,
	appName string,
	repo *backupv1alpha1.KopiaRepository,
) *batchv1.CronJob {
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
	var kopiaCacheDirectory = repo.Spec.Caching.CacheDirectory
	var kopiaLogDir = filepath.Join(repo.Spec.FileSystemOptions.Path, ".kopia", "logs")

	// Base env vars - KOPIA_CACHE_DIRECTORY will be set differently for server vs direct mode
	var envVars = []corev1.EnvVar{
		{
			Name:  "KOPIA_LOG_DIR",
			Value: kopiaLogDir,
		},
	}

	var envFrom = []corev1.EnvFromSource{}

	var volumeMounts = []corev1.VolumeMount{
		{
			Name:      "data",
			MountPath: mountPath,
		},
	}

	var volumes = []corev1.Volume{
		{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: backup.Spec.PVCName,
				},
			},
		},
	}

	// Handle server mode vs direct mode
	if repo.Spec.Server.Enabled {
		// Server mode - mount user credentials secret
		secretName := fmt.Sprintf("kopia-backup-user-%s-%s", backup.Namespace, backup.Spec.PVCName)

		// Add environment variables from credentials secret
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: secretName,
				},
			},
		})

		// Set KOPIA_PASSWORD from KOPIA_SERVER_PASSWORD in the secret
		envVars = append(envVars, corev1.EnvVar{
			Name: "KOPIA_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "KOPIA_SERVER_PASSWORD",
				},
			},
		})

		// Set KOPIA_TLS_FINGERPRINT from the repository status
		// The fingerprint is stored in the repository status after the TLS secret is created
		if repo.Status.TLSCertFingerprint != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "KOPIA_TLS_FINGERPRINT",
				Value: repo.Status.TLSCertFingerprint,
			})
		}

		// Add kopia cache volume (emptyDir)
		// Mount at /cache and use /cache/kopia as the actual cache directory
		// This allows kopia to delete the cache subdirectory on disconnect
		volumes = append(volumes, corev1.Volume{
			Name: "kopia-cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: resource.NewQuantity(3<<30, resource.BinarySI), // 3GiB
				},
			},
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "kopia-cache",
			MountPath: "/cache",
		})

		// Override the cache directory to use a subdirectory of the mount
		// so kopia can delete it on disconnect without hitting "device busy"
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KOPIA_CACHE_DIRECTORY",
			Value: "/cache/kopia",
		})
	} else {
		// Direct mode - existing behavior
		// Set cache directory from repo spec
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KOPIA_CACHE_DIRECTORY",
			Value: kopiaCacheDirectory,
		})

		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "config",
			MountPath: "/config/repository.config",
			SubPath:   "repository.config",
		})

		switch repo.Spec.StorageType {
		case storageTypeFilesystem:
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      "repo",
				MountPath: repo.Spec.FileSystemOptions.Path,
			})

			// Mount the configmap to the pod
			volumes = append(volumes, corev1.Volume{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: fmt.Sprintf("kopia-config-%s", repo.Name),
						},
					},
				},
			})

			// Mount the NFS volume to the pod
			volumes = append(volumes, corev1.Volume{
				Name: "repo",
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: repo.Spec.FileSystemOptions.NFSServer,
						Path:   repo.Spec.FileSystemOptions.NFSPath,
					},
				},
			})

		case storageTypeSFTP:
			volumes = append(volumes,
				// SFTP credentials from secret
				corev1.Volume{
					Name: "sftp-credentials",
					VolumeSource: corev1.VolumeSource{
						Secret: &corev1.SecretVolumeSource{
							SecretName:  repo.Spec.SFTPOptions.CredentialsSecret,
							DefaultMode: func(i int32) *int32 { return &i }(0600),
						},
					},
				},
				// Config volume for repository.config
				corev1.Volume{
					Name: "config",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				// emptyDir for kopia cache
				corev1.Volume{
					Name: "kopia-cache",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							SizeLimit: resource.NewQuantity(3<<30, resource.BinarySI), // 3GiB
						},
					},
				},
			)

			volumeMounts = append(volumeMounts,
				corev1.VolumeMount{
					Name:      "sftp-credentials",
					MountPath: "/sftp-creds",
					ReadOnly:  true,
				},
				corev1.VolumeMount{
					Name:      "kopia-cache",
					MountPath: kopiaCacheDirectory,
				},
			)
		}

		// Add repository password for direct mode
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
	}

	// Add an init container with the following spec:
	// initContainers:
	//  - name: wait
	//  image: ghcr.io/fastlorenzo/kopia:0.16.1@sha256:e473aeb43e13e298853898c3613da2a4834f4bff2ccf747fbb2a90072d9e92c8
	//  command: ["/scripts/sleep.sh"]
	//  args: ["1", "900"]

	// TODO: parameterize the sleep time
	var initContainers []corev1.Container
	initContainers = append(initContainers, corev1.Container{
		Name:    "wait",
		Image:   "ghcr.io/fastlorenzo/kopia:0.16.1@sha256:e473aeb43e13e298853898c3613da2a4834f4bff2ccf747fbb2a90072d9e92c8",
		Command: []string{"/scripts/sleep.sh"},
		Args:    []string{"1", "10"},
	})

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cronJobName,
			Namespace: backup.Namespace,
		},
		Spec: batchv1.CronJobSpec{
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			Schedule:                   backup.Spec.Schedule,
			Suspend:                    &backup.Spec.Suspend,
			SuccessfulJobsHistoryLimit: func(i int32) *int32 { return &i }(maxBackupHistoryEntries),
			FailedJobsHistoryLimit:     func(i int32) *int32 { return &i }(maxBackupHistoryEntries),
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"backup.cloudinfra.be/pvc-name":  backup.Spec.PVCName,
								"backup.cloudinfra.be/node-name": nodeName,
								"app.kubernetes.io/name":         appName,
								"sidecar.istio.io/inject":        "false",
							},
						},
						Spec: corev1.PodSpec{
							// NodeName:       nodeName,
							Affinity: &corev1.Affinity{
								NodeAffinity: &corev1.NodeAffinity{
									RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
										NodeSelectorTerms: []corev1.NodeSelectorTerm{
											{
												MatchExpressions: []corev1.NodeSelectorRequirement{
													{
														Key:      "kubernetes.io/hostname",
														Operator: corev1.NodeSelectorOpIn,
														Values:   []string{nodeName},
													},
												},
											},
										},
									},
								},
							},
							InitContainers: initContainers,
							Containers: []corev1.Container{
								{
									Name:         "snapshot",
									Image:        "ghcr.io/fastlorenzo/kopia:0.20.1@sha256:4a2660db62960eb0b4ba98982c4566bcc9dd2ee3b15b31af9626146aa4e5d8e3",
									Args:         []string{"/bin/bash", "-c", buildBackupCommand(backup, repo, mountPath)},
									Env:          envVars,
									EnvFrom:      envFrom,
									VolumeMounts: volumeMounts,
								},
							},
							Volumes:       volumes,
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

func constructConfigMap(backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) *corev1.ConfigMap {
	configData := fmt.Sprintf(`{
        "storage": {
            "type": "%s",
            "config": {
                "path": "%s",
                "dirShards": null
            }
        },
        "caching": {
            "cacheDirectory": "%s",
            "maxCacheSize": %d,
            "maxMetadataCacheSize": %d,
            "maxListCacheDuration": %d
        },
        "hostname": "%s",
        "username": "%s",
        "description": "%s",
        "enableActions": %t,
        "formatBlobCacheDuration": %d
    }`,
		repo.Spec.StorageType,
		repo.Spec.FileSystemOptions.Path,
		repo.Spec.Caching.CacheDirectory,
		repo.Spec.Caching.ContentCacheSizeBytes,
		repo.Spec.Caching.MetadataCacheSizeBytes,
		repo.Spec.Caching.MaxListCacheDuration,
		repo.Spec.Hostname,
		repo.Spec.Username,
		repo.Spec.Description,
		repo.Spec.EnableActions,
		repo.Spec.FormatBlobCacheDuration,
	)

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("kopia-config-%s", repo.Name),
			Namespace: backup.Namespace,
			Labels: map[string]string{
				"backup.cloudinfra.be/pvc-name": backup.Spec.PVCName,
			},
		},
		Data: map[string]string{
			"repository.config": configData,
		},
	}

	return configMap
}

func getKopiaRepositoryByName(
	ctx context.Context,
	c client.Client,
	repositoryName string,
	log logr.Logger,
) (*backupv1alpha1.KopiaRepository, error) {
	// Check if the repository exists in the current namespace
	repository := &backupv1alpha1.KopiaRepository{}
	err := c.Get(ctx, types.NamespacedName{Name: repositoryName}, repository)

	if err != nil {
		// Real error, return it
		if !apierrors.IsNotFound(err) {
			log.Error(err, "error getting KopiaRepository", "repositoryName", repositoryName)
			return nil, err
		}

		// If not, try to list all repositories in all namespaces and find the repository
		log.Info("KopiaRepository not found in the current namespace, checking all namespaces", "currentNamespace", ctx.Value("namespace"))
		var allRepositories backupv1alpha1.KopiaRepositoryList
		if err := c.List(ctx, &allRepositories); err != nil {
			log.Error(err, "Error listing KopiaRepositories")
			return nil, err
		}

		log.Info("found KopiaRepositories", "KopiaRepositoriesCount", len(allRepositories.Items))
		var matchingRepositories []backupv1alpha1.KopiaRepository
		for _, repo := range allRepositories.Items {
			if repo.Name == repositoryName {
				matchingRepositories = append(matchingRepositories, repo)
			}
		}

		log.Info("found matching KopiaRepositories", "MatchingKopiaRepositories", len(matchingRepositories))

		// repositories, err := getKopiaRepositoriesByName(ctx, r.Client, repositoryName)
		// if err != nil {
		//  log.Error(err, "error listing KopiaRepositories")
		//  return ctrl.Result{}, err
		// }
		if len(matchingRepositories) == 0 {
			log.Info("KopiaRepository not found in all namespaces", "repositoryName", repositoryName)
			return nil, fmt.Errorf("KopiaRepository '%s' not found", repositoryName)
		}
		if len(matchingRepositories) > 1 {
			log.Error(nil, "multiple KopiaRepositories with the same name found in multiple namespaces", "repositoryName", repositoryName)
			return nil, fmt.Errorf("multiple KopiaRepositories with the same name found in multiple namespaces")
		}
		repository = &matchingRepositories[0]
	}

	log.Info("Found KopiaRepository", "repositoryName", repository.Name, "repositoryNamespace", repository.Namespace)
	return repository, nil
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
