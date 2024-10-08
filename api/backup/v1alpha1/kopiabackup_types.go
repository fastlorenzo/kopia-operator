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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// KopiaBackupSpec defines the desired state of KopiaBackup
type KopiaBackupSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Name of the PVC to backup
	PVCName string `json:"pvcName"`
	// Schedule for the backup
	Schedule string `json:"schedule"`
	// KopiaRepository to use for the backup
	Repository string `json:"repository"`

	// Optional: suspend (default=false) will suspend the cronjob
	Suspend bool `json:"suspend,omitempty"`
}

// KopiaBackupStatus defines the observed state of KopiaBackup
type KopiaBackupStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Active         bool `json:"active"`
	FromAnnotation bool `json:"fromAnnotation"`
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
