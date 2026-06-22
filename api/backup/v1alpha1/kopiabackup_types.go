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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ConditionTypeReady indicates the backup is fully reconciled and the CronJob is active.
	ConditionTypeReady = "Ready"
	// ConditionTypeCronJobCreated indicates the CronJob has been created for this backup.
	ConditionTypeCronJobCreated = "CronJobCreated"

	// ReasonReconciled indicates the resource was reconciled successfully.
	ReasonReconciled = "Reconciled"
	// ReasonPVCNotFound indicates the referenced PVC was not found.
	ReasonPVCNotFound = "PVCNotFound"
	// ReasonRepositoryNotFound indicates the referenced KopiaRepository was not found.
	ReasonRepositoryNotFound = "RepositoryNotFound"
	// ReasonNoPodFound indicates no running pod was found using the PVC.
	ReasonNoPodFound = "NoPodFound"
	// ReasonCronJobFailed indicates the CronJob could not be created or updated.
	ReasonCronJobFailed = "CronJobFailed"
	// ReasonSuspended indicates the backup is suspended.
	ReasonSuspended = "Suspended"
)

// BackupStatus represents the status of a backup operation.
// +kubebuilder:validation:Enum=Successful;Failed;Pending;InProgress
type BackupStatus string

const (
	BackupStatusSuccessful BackupStatus = "Successful"
	BackupStatusFailed     BackupStatus = "Failed"
	BackupStatusPending    BackupStatus = "Pending"
	BackupStatusInProgress BackupStatus = "InProgress"
)

// BackupHistoryEntry represents a single backup execution record.
type BackupHistoryEntry struct {
	// Timestamp of when the backup was started.
	StartTime metav1.Time `json:"startTime"`
	// Timestamp of when the backup completed (nil if still running).
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// Status of the backup.
	Status BackupStatus `json:"status"`
	// Name of the Job that executed this backup.
	// +optional
	JobName string `json:"jobName,omitempty"`
	// Additional details about the backup status.
	// +optional
	Message string `json:"message,omitempty"`
}

// KopiaBackupSpec defines the desired state of KopiaBackup.
type KopiaBackupSpec struct {
	// Name of the PVC to back up.
	// +kubebuilder:validation:MinLength=1
	PVCName string `json:"pvcName"`

	// Cron schedule for the backup (e.g. "0 3 * * *").
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Name of the KopiaRepository to use for this backup.
	// +kubebuilder:validation:MinLength=1
	Repository string `json:"repository"`

	// Suspend the CronJob when set to true.
	// +kubebuilder:default:=false
	Suspend bool `json:"suspend,omitempty"`

	// SuccessfulJobsHistoryLimit is the number of successful finished jobs to retain.
	// Defaults to 3, matching the Kubernetes CronJob default.
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	SuccessfulJobsHistoryLimit *int32 `json:"successfulJobsHistoryLimit,omitempty"`

	// FailedJobsHistoryLimit is the number of failed finished jobs to retain.
	// Defaults to 1, matching the Kubernetes CronJob default.
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	FailedJobsHistoryLimit *int32 `json:"failedJobsHistoryLimit,omitempty"`

	// TTLSecondsAfterFinished is the time-to-live, in seconds, for a finished
	// (completed or failed) backup Job before it is automatically deleted. This
	// keeps finished Jobs from accumulating indefinitely. Defaults to 86400 (24h).
	// +kubebuilder:default:=86400
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// ActiveDeadlineSeconds is the maximum duration, in seconds, a backup Job may
	// run before it is terminated and marked failed. This guards against a pod
	// that gets stuck (e.g. never leaves Init) blocking all future runs under the
	// Forbid concurrency policy. Defaults to 21600 (6h).
	// +kubebuilder:default:=21600
	// +kubebuilder:validation:Minimum=1
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// BackoffLimit is the number of retries before a backup Job is marked failed.
	// Defaults to 3.
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// Server user credentials secret name (auto-populated by the operator in server mode).
	// Contains username and password for authenticating to the Kopia Server.
	// +optional
	UserCredentialsSecret string `json:"userCredentialsSecret,omitempty"`
}

// KopiaBackupStatus defines the observed state of KopiaBackup.
type KopiaBackupStatus struct {
	// Conditions represent the latest available observations of the backup's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Whether this KopiaBackup was auto-created from a PVC label.
	// +optional
	AutoCreated bool `json:"autoCreated,omitempty"`

	// Name of the CronJob managed by this backup.
	// +optional
	CronJobName string `json:"cronJobName,omitempty"`

	// Node where the pod using the PVC is running.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Server URL when server mode is enabled.
	// +optional
	ServerURL string `json:"serverURL,omitempty"`

	// Username for server authentication.
	// +optional
	Username string `json:"username,omitempty"`

	// Whether the backup is connected to the server.
	// +optional
	Connected bool `json:"connected,omitempty"`

	// Status of the last backup.
	// +optional
	LastBackupStatus BackupStatus `json:"lastBackupStatus,omitempty"`

	// Timestamp of the last backup attempt (regardless of outcome).
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// Timestamp of the last successful backup.
	// +optional
	LastSuccessfulBackupTime *metav1.Time `json:"lastSuccessfulBackupTime,omitempty"`

	// History of the last 10 backup executions.
	// +optional
	BackupHistory []BackupHistoryEntry `json:"backupHistory,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=kb
// +kubebuilder:printcolumn:name="PVC",type=string,JSONPath=`.spec.pvcName`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Repository",type=string,JSONPath=`.spec.repository`
// +kubebuilder:printcolumn:name="Suspended",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KopiaBackup is the Schema for the kopiabackups API.
type KopiaBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopiaBackupSpec   `json:"spec,omitempty"`
	Status KopiaBackupStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KopiaBackupList contains a list of KopiaBackup.
type KopiaBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopiaBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopiaBackup{}, &KopiaBackupList{})
}
