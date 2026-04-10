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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	// ConditionTypeServerReady indicates the Kopia Server is deployed and ready.
	ConditionTypeServerReady = "ServerReady"

	// ReasonConfigValid indicates the repository configuration passed validation.
	ReasonConfigValid = "ConfigValid"
	// ReasonMissingPassword indicates no password secret reference was provided.
	ReasonMissingPassword = "MissingPassword"
	// ReasonUnsupportedStorage indicates an unsupported storage type.
	ReasonUnsupportedStorage = "UnsupportedStorage"
	// ReasonServerDeployed indicates the Kopia Server is deployed and running.
	ReasonServerDeployed = "ServerDeployed"
	// ReasonServerFailed indicates the Kopia Server failed to deploy.
	ReasonServerFailed = "ServerFailed"
)

// KopiaServerTLSSpec defines TLS configuration for the Kopia Server.
// TLS is always enabled as Kopia requires HTTPS for server connections.
type KopiaServerTLSSpec struct {
	// Name of the secret containing TLS certificate and key.
	// Secret should contain 'tls.crt' and 'tls.key' keys.
	// If not provided, a self-signed certificate will be auto-generated.
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// CertificateCommonName is the CN for the auto-generated certificate.
	// Defaults to the service name.
	// +optional
	CertificateCommonName string `json:"certificateCommonName,omitempty"`

	// CertificateDNSNames are additional DNS names for the auto-generated certificate.
	// The service name is always included automatically.
	// +optional
	CertificateDNSNames []string `json:"certificateDNSNames,omitempty"`
}

// KopiaServerExposureSpec defines how the Kopia Server should be exposed.
type KopiaServerExposureSpec struct {
	// Type of exposure.
	// +kubebuilder:validation:Enum=Service
	// +kubebuilder:default:=Service
	Type string `json:"type,omitempty"`

	// Kubernetes Service type.
	// +kubebuilder:default:=ClusterIP
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`

	// Port for the Kopia Server service.
	// +kubebuilder:default:=51515
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	ServicePort int32 `json:"servicePort,omitempty"`
}

// KopiaServerSpec defines the configuration for running Kopia in server mode.
type KopiaServerSpec struct {
	// Enable Kopia Server mode.
	// When enabled, the operator deploys a Kopia Server for this repository
	// and backups connect through the server instead of directly to storage.
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled"`

	// Container image for the Kopia Server.
	// +kubebuilder:default:="ghcr.io/fastlorenzo/kopia:latest"
	Image string `json:"image,omitempty"`

	// Number of server replicas.
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`

	// Resource requirements for the server.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// TLS configuration for the server.
	// +optional
	TLS KopiaServerTLSSpec `json:"tls,omitempty"`

	// Exposure configuration (how to expose the server).
	// +optional
	Exposure KopiaServerExposureSpec `json:"exposure,omitempty"`

	// Name of a Secret containing the server admin password (key: password).
	// If not provided, the repository password secret is used.
	// +optional
	AdminPasswordSecretName string `json:"adminPasswordSecretName,omitempty"`

	// PersistentVolumeClaim for server internal state.
	// If not provided, server uses emptyDir (state lost on restart).
	// +optional
	PersistentVolumeClaim string `json:"persistentVolumeClaim,omitempty"`

	// Additional command-line arguments for kopia server start.
	// +optional
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

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
	// Path to the repository on the SFTP server.
	Path string `json:"path"`

	// SFTP server hostname.
	Host string `json:"host"`

	// SFTP server port.
	// +kubebuilder:default:=22
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int `json:"port,omitempty"`

	// Known hosts data for SSH host key verification.
	// +optional
	KnownHostsData string `json:"knownHostsData,omitempty"`

	// Use external SSH command instead of built-in SSH.
	// +kubebuilder:default:=false
	ExternalSSH bool `json:"externalSSH,omitempty"`

	// SSH command to use when ExternalSSH is true.
	// +kubebuilder:default:="ssh"
	SSHCommand string `json:"sshCommand,omitempty"`

	// Directory shards configuration.
	// +optional
	DirShards []int `json:"dirShards,omitempty"`

	// Name of Secret containing SFTP credentials.
	// Expected keys: username, password (optional), keyData (optional - SSH private key).
	// At least one of password or keyData must be provided.
	CredentialsSecret string `json:"credentialsSecret"`
}

// KopiaRepositoryCachingSpec defines caching options for the repository.
type KopiaRepositoryCachingSpec struct {
	// Directory used for local caching.
	// +kubebuilder:default:="cache"
	CacheDirectory string `json:"cacheDirectory,omitempty"`

	// Maximum size of the content cache.
	// +kubebuilder:default:="5000Mi"
	ContentCacheSize resource.Quantity `json:"contentCacheSize,omitempty"`

	// Hard limit for content cache size.
	// +optional
	ContentCacheSizeLimit resource.Quantity `json:"contentCacheSizeLimit,omitempty"`

	// Maximum size of the metadata cache.
	// +kubebuilder:default:="5000Mi"
	MetadataCacheSize resource.Quantity `json:"metadataCacheSize,omitempty"`

	// Hard limit for metadata cache size.
	// +optional
	MetadataCacheSizeLimit resource.Quantity `json:"metadataCacheSizeLimit,omitempty"`

	// Maximum duration (in seconds) to cache directory listings.
	// +kubebuilder:default:=30
	// +kubebuilder:validation:Minimum=0
	MaxListCacheDuration int64 `json:"maxListCacheDuration,omitempty"`

	// Minimum age (in seconds) of metadata before it can be swept.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinMetadataSweepAge int64 `json:"minMetadataSweepAge,omitempty"`

	// Minimum age (in seconds) of content before it can be swept.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinContentSweepAge int64 `json:"minContentSweepAge,omitempty"`

	// Minimum age (in seconds) of index entries before they can be swept.
	// +kubebuilder:validation:Minimum=0
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
	ReadOnly bool `json:"readOnly,omitempty"`

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

	// Duration (in seconds) to cache format blobs.
	// +kubebuilder:default:=900
	FormatBlobCacheDurationSeconds int64 `json:"formatBlobCacheDurationSeconds,omitempty"`

	// Caching options for the repository.
	// +kubebuilder:default:={}
	Caching KopiaRepositoryCachingSpec `json:"caching,omitempty"`

	// Filesystem storage options (required when storageType is "filesystem").
	// +optional
	FileSystemOptions KopiaRepositoryStorageFileSystemSpec `json:"fileSystemOptions,omitempty"`

	// SFTP storage options (required when storageType is "sftp").
	// +optional
	SFTPOptions KopiaRepositoryStorageSFTPSpec `json:"sftpOptions,omitempty"`

	// Server configuration for running Kopia in server mode.
	// +optional
	Server KopiaServerSpec `json:"server,omitempty"`
}

// KopiaRepositoryStatus defines the observed state of KopiaRepository.
type KopiaRepositoryStatus struct {
	// Conditions represent the latest available observations of the repository's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Whether the Kopia Server is deployed and ready (server mode only).
	// +optional
	ServerReady bool `json:"serverReady,omitempty"`

	// URL to connect to the Kopia Server.
	// +optional
	ServerURL string `json:"serverURL,omitempty"`

	// Deployment name of the Kopia Server.
	// +optional
	ServerDeployment string `json:"serverDeployment,omitempty"`

	// Service name for the Kopia Server.
	// +optional
	ServerService string `json:"serverService,omitempty"`

	// SHA256 fingerprint of the server's TLS certificate (uppercase hex without colons).
	// Used by clients to verify the server's certificate.
	// +optional
	TLSCertFingerprint string `json:"tlsCertFingerprint,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Storage",type=string,JSONPath=`.spec.storageType`
// +kubebuilder:printcolumn:name="Hostname",type=string,JSONPath=`.spec.hostname`
// +kubebuilder:printcolumn:name="Server",type=boolean,JSONPath=`.spec.server.enabled`
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
