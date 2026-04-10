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

// StorageType defines the backend storage type for a Kopia repository.
// +kubebuilder:validation:Enum=filesystem;sftp
type StorageType string

const (
	StorageTypeFilesystem StorageType = "filesystem"
	StorageTypeSFTP       StorageType = "sftp"
)

const (
	// ConditionTypeRepositoryReady indicates the repository configuration is valid.
	ConditionTypeRepositoryReady = "Ready"

	// ReasonConfigValid indicates the repository configuration passed validation.
	ReasonConfigValid = "ConfigValid"
	// ReasonMissingPassword indicates no password secret reference was provided.
	ReasonMissingPassword = "MissingPassword"
	// ReasonUnsupportedStorage indicates an unsupported storage type.
	ReasonUnsupportedStorage = "UnsupportedStorage"
)

// KopiaRepositoryStorageFileSystemSpec configures a filesystem-backed repository.
type KopiaRepositoryStorageFileSystemSpec struct {
	// Path to the repository on the filesystem.
	Path string `json:"path"`

	// FileMode for files in the repository.
	// +optional
	FileMode uint32 `json:"fileMode,omitempty"`
	// DirectoryMode for directories in the repository.
	// +optional
	DirectoryMode uint32 `json:"dirMode,omitempty"`

	// UID of files in the repository.
	// +optional
	FileUID int `json:"uid,omitempty"`
	// GID of files in the repository.
	// +optional
	FileGID int `json:"gid,omitempty"`

	// NFS export path for the repository.
	// +optional
	NFSPath string `json:"nfsPath,omitempty"`
	// NFS server address.
	// +optional
	NFSServer string `json:"nfsServer,omitempty"`
}

// KopiaRepositoryStorageSFTPSpec configures an SFTP-backed repository.
type KopiaRepositoryStorageSFTPSpec struct {
	// Name of the ConfigMap containing the SFTP configuration.
	// +optional
	ConfigMapName string `json:"configMapName,omitempty"`
}

// KopiaRepositoryCachingSpec defines caching options for the repository.
type KopiaRepositoryCachingSpec struct {
	// Directory used for local caching.
	// +kubebuilder:default:="cache"
	CacheDirectory string `json:"cacheDirectory,omitempty"`

	// Maximum size of the content cache in bytes.
	// +kubebuilder:default:=5242880000
	ContentCacheSizeBytes int64 `json:"maxCacheSize,omitempty"`

	// Hard limit for content cache size in bytes.
	// +optional
	ContentCacheSizeLimitBytes int64 `json:"contentCacheSizeLimitBytes,omitempty"`

	// Maximum size of the metadata cache in bytes.
	// +kubebuilder:default:=5242880000
	MetadataCacheSizeBytes int64 `json:"maxMetadataCacheSize,omitempty"`

	// Hard limit for metadata cache size in bytes.
	// +optional
	MetadataCacheSizeLimitBytes int64 `json:"metadataCacheSizeLimitBytes,omitempty"`

	// Maximum duration (in seconds) to cache directory listings.
	// +kubebuilder:default:=30
	MaxListCacheDuration int64 `json:"maxListCacheDuration,omitempty"`

	// Minimum age (in seconds) of metadata before it can be swept.
	// +optional
	MinMetadataSweepAge int64 `json:"minMetadataSweepAge,omitempty"`

	// Minimum age (in seconds) of content before it can be swept.
	// +optional
	MinContentSweepAge int64 `json:"minContentSweepAge,omitempty"`

	// Minimum age (in seconds) of index entries before they can be swept.
	// +optional
	MinIndexSweepAge int64 `json:"minIndexSweepAge,omitempty"`
}

// KopiaRepositorySpec defines the desired state of KopiaRepository.
type KopiaRepositorySpec struct {
	// Hostname used by Kopia to identify this repository.
	// +kubebuilder:validation:MinLength=1
	Hostname string `json:"hostname"`

	// Username used by Kopia to identify the repository owner.
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`

	// Backend storage type.
	StorageType StorageType `json:"storageType"`

	// Make the repository read-only.
	// +optional
	ReadOnly bool `json:"readonly,omitempty"`

	// Allow loading from cache even when stale.
	// +optional
	PermissiveCacheLoading bool `json:"permissiveCacheLoading,omitempty"`

	// Human-readable description shown in the Kopia UI.
	// +kubebuilder:default:=Cluster
	Description string `json:"description,omitempty"`

	// Enable Kopia actions in the repository.
	// +kubebuilder:default:=false
	EnableActions bool `json:"enableActions,omitempty"`

	// Default cron schedule for KopiaBackup resources that omit their own schedule.
	// +optional
	DefaultSchedule string `json:"defaultSchedule,omitempty"`

	// Name of an existing Secret containing the repository password in the KOPIA_PASSWORD key.
	// The Secret must be in the same namespace as the KopiaRepository.
	// +kubebuilder:validation:MinLength=1
	PasswordSecretName string `json:"passwordSecretName"`

	// Duration (in nanoseconds) to cache format blobs.
	// +kubebuilder:default:=900000000000
	FormatBlobCacheDuration int64 `json:"formatBlobCacheDuration,omitempty"`

	// Caching options for the repository.
	// +kubebuilder:default:={}
	Caching KopiaRepositoryCachingSpec `json:"caching,omitempty"`

	// Filesystem storage options (required when storageType is "filesystem").
	// +optional
	FileSystemOptions KopiaRepositoryStorageFileSystemSpec `json:"fileSystemOptions,omitempty"`

	// SFTP storage options (required when storageType is "sftp").
	// +optional
	SFTPOptions KopiaRepositoryStorageSFTPSpec `json:"sftpOptions,omitempty"`
}

// KopiaRepositoryStatus defines the observed state of KopiaRepository.
type KopiaRepositoryStatus struct {
	// Conditions represent the latest available observations of the repository's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=`.spec.storageType`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// KopiaRepository is the Schema for the kopiarepositories API.
type KopiaRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopiaRepositorySpec   `json:"spec,omitempty"`
	Status KopiaRepositoryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KopiaRepositoryList contains a list of KopiaRepository.
type KopiaRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopiaRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopiaRepository{}, &KopiaRepositoryList{})
}
