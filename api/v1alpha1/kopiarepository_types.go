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

type KopiaRepositoryStorageConfigSpec struct {
	Path string `json:"path"`
}

type KopiaRepositoryStorageFileSystemSpec struct {
	// Path is the path to the repository on the filesystem.
	Path string `json:"path"`

	// FileMode in the repository.
	FileMode uint32 `json:"fileMode,omitempty"`
	// DirectoryMode in the repository.
	DirectoryMode uint32 `json:"dirMode,omitempty"`

	// User ID of the files in the repository.
	FileUID int `json:"uid,omitempty"`
	// Group ID of the files in the repository.
	FileGID int `json:"gid,omitempty"`

	// Export path on the NFS server for the repository.
	NFSPath string `json:"nfsPath,omitempty"`
	// NFS server for the repository.
	NFSServer string `json:"nfsServer,omitempty"`
}

// KopiaRepositoryCachingSpec defines the desired state of KopiaRepositoryCaching
type KopiaRepositoryCachingSpec struct {
	CacheDirectory              string `json:"cacheDirectory,omitempty"`
	ContentCacheSizeBytes       int64  `json:"maxCacheSize,omitempty"`
	ContentCacheSizeLimitBytes  int64  `json:"contentCacheSizeLimitBytes,omitempty"`
	MetadataCacheSizeBytes      int64  `json:"maxMetadataCacheSize,omitempty"`
	MetadataCacheSizeLimitBytes int64  `json:"metadataCacheSizeLimitBytes,omitempty"`
	MaxListCacheDuration        int64  `json:"maxListCacheDuration,omitempty"`
	MinMetadataSweepAge         int64  `json:"minMetadataSweepAge,omitempty"`
	MinContentSweepAge          int64  `json:"minContentSweepAge,omitempty"`
	MinIndexSweepAge            int64  `json:"minIndexSweepAge,omitempty"`
	// HMACSecret                  []byte `json:"-"`
}

// KopiaRepositorySpec defines the desired state of KopiaRepository
type KopiaRepositorySpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Kopia repository hostname
	Hostname string `json:"hostname"`
	// Kopia repository username
	Username string `json:"username"`
	// Storage type (currently only filesystem is supported)
	StorageType string `json:"storageType"`

	// Make the repository read-only
	ReadOnly bool `json:"readonly,omitempty"`
	// Allow loading from cache even if it's stale
	PermissiveCacheLoading bool `json:"permissiveCacheLoading,omitempty"`
	// Human-readable description of the repository to use in the UI.
	Description string `json:"description,omitempty"`
	// Enables Kopia actions in the repository.
	EnableActions bool `json:"enableActions"`

	// Password for Kopia repository, ignored if RepositoryPasswordExistingSecret is set
	RepositoryPassword string `json:"repositoryPassword,omitempty"`
	// Secret name containing the password for the Kopia repository (must be in the same namespace); the password should be in KOPIA_PASSWORD key
	RepositoryPasswordExistingSecret string `json:"repositoryPasswordExistingSecret,omitempty"`

	// FormatBlobCacheDuration time.Duration              `json:"formatBlobCacheDuration,omitempty"`
	Caching KopiaRepositoryCachingSpec `json:"caching,omitempty"`

	FileSystemOptions KopiaRepositoryStorageFileSystemSpec `json:"fileSystemOptions,omitempty"`
}

// KopiaRepositoryStatus defines the observed state of KopiaRepository
type KopiaRepositoryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// KopiaRepository is the Schema for the kopiarepositories API
type KopiaRepository struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KopiaRepositorySpec   `json:"spec,omitempty"`
	Status KopiaRepositoryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// KopiaRepositoryList contains a list of KopiaRepository
type KopiaRepositoryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KopiaRepository `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KopiaRepository{}, &KopiaRepositoryList{})
}
