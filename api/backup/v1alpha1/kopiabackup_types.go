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

// KopiaBackupSpec defines the desired state of KopiaBackup
type KopiaBackupSpec struct {
	// Name of the PVC to backup
	PVCName string `json:"pvcName"`
	// Schedule for the backup
	Schedule string `json:"schedule"`
	// KopiaRepository to use for the backup
	Repository string `json:"repository"`

	// Optional: suspend (default=false) will suspend the cronjob
	Suspend bool `json:"suspend,omitempty"`

	// Server user credentials secret name (auto-generated, read-only)
	// This field is populated by the operator when server mode is enabled
	// Contains username and password for authenticating to the Kopia Server
	UserCredentialsSecret string `json:"userCredentialsSecret,omitempty"`
}

// BackupStatus represents the status of a backup operation
// +kubebuilder:validation:Enum=Successful;Failed;Pending;InProgress
type BackupStatus string

const (
	// BackupStatusSuccessful indicates the backup completed successfully
	BackupStatusSuccessful BackupStatus = "Successful"
	// BackupStatusFailed indicates the backup failed
	BackupStatusFailed BackupStatus = "Failed"
	// BackupStatusPending indicates the backup has not run yet
	BackupStatusPending BackupStatus = "Pending"
	// BackupStatusInProgress indicates the backup is currently running
	BackupStatusInProgress BackupStatus = "InProgress"
)

// BackupHistoryEntry represents a single backup execution record
type BackupHistoryEntry struct {
	// Timestamp of when the backup was started
	StartTime metav1.Time `json:"startTime"`
	// Timestamp of when the backup completed (nil if still running)
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// Status of the backup (Successful, Failed, InProgress)
	Status BackupStatus `json:"status"`
	// Name of the Job that executed this backup
	JobName string `json:"jobName,omitempty"`
	// Message providing additional details about the backup status
	Message string `json:"message,omitempty"`
}

// KopiaBackupStatus defines the observed state of KopiaBackup
type KopiaBackupStatus struct {
	Active         bool `json:"active"`
	FromAnnotation bool `json:"fromAnnotation"`

	// Server connection status (when server mode is enabled)
	ServerURL string `json:"serverURL,omitempty"`

	// Username used for server authentication
	Username string `json:"username,omitempty"`

	// Whether the backup is connected to the server
	Connected bool `json:"connected,omitempty"`

	// Status of the last backup (Successful, Failed, Pending, InProgress)
	// +optional
	LastBackupStatus BackupStatus `json:"lastBackupStatus,omitempty"`

	// Timestamp of the last backup attempt (regardless of success or failure)
	// +optional
	LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`

	// Timestamp of the last successful backup
	// +optional
	LastSuccessfulBackupTime *metav1.Time `json:"lastSuccessfulBackupTime,omitempty"`

	// History of the last 10 backup executions
	// +optional
	BackupHistory []BackupHistoryEntry `json:"backupHistory,omitempty"`

	// Conditions represent the latest available observations of the backup's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// KopiaBackup is the Schema for the kopiabackups API
type KopiaBackup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopiaBackupSpec   `json:"spec,omitempty"`
	Status KopiaBackupStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KopiaBackupList contains a list of KopiaBackup
type KopiaBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopiaBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopiaBackup{}, &KopiaBackupList{})
}
