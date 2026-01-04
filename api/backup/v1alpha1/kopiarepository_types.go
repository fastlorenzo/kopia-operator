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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// KopiaServerTLSSpec defines TLS configuration for the Kopia Server
type KopiaServerTLSSpec struct {
	// Enable TLS for the server
	// +kubebuilder:default:=true
	Enabled bool `json:"enabled"`

	// Name of the secret containing TLS certificate and key
	// Secret should contain 'tls.crt' and 'tls.key' keys
	SecretName string `json:"secretName,omitempty"`

	// Auto-generate self-signed certificate if secret not provided
	// +kubebuilder:default:=true
	AutoGenerate bool `json:"autoGenerate,omitempty"`
}

// KopiaServerExposureSpec defines how the Kopia Server should be exposed
type KopiaServerExposureSpec struct {
	// Type of exposure: Service only for now
	// TODO: Add Ingress and HTTPRoute support later
	// +kubebuilder:validation:Enum=Service;""
	// +kubebuilder:default:=Service
	Type string `json:"type,omitempty"`

	// Service configuration
	// +kubebuilder:default:=ClusterIP
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`
	// +kubebuilder:default:=51515
	ServicePort int32 `json:"servicePort,omitempty"`

	// TODO: Ingress configuration (commented out for now)
	// IngressClassName string            `json:"ingressClassName,omitempty"`
	// Host             string            `json:"host,omitempty"`
	// Annotations      map[string]string `json:"annotations,omitempty"`

	// TODO: HTTPRoute configuration (commented out for now)
	// GatewayName      string `json:"gatewayName,omitempty"`
	// GatewayNamespace string `json:"gatewayNamespace,omitempty"`
}

// KopiaServerSpec defines the configuration for running Kopia in server mode
type KopiaServerSpec struct {
	// Enable Kopia Server mode
	// When enabled, the operator will deploy a Kopia Server for this repository
	// and backups will connect through the server instead of directly to storage
	// +kubebuilder:default:=false
	Enabled bool `json:"enabled"`

	// Container image for the Kopia Server
	// +kubebuilder:default:="ghcr.io/fastlorenzo/kopia:latest"
	Image string `json:"image,omitempty"`

	// Number of server replicas
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=1
	Replicas int32 `json:"replicas,omitempty"`

	// Resource requirements for the server
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// TLS configuration for the server
	TLS KopiaServerTLSSpec `json:"tls,omitempty"`

	// Exposure configuration (how to expose the server)
	Exposure KopiaServerExposureSpec `json:"exposure,omitempty"`

	// Server admin password for server control and user management
	// If not provided, will use the repository password
	ServerAdminPassword string `json:"serverAdminPassword,omitempty"`

	// Secret containing server admin password
	// Expected key: password
	// If provided, takes precedence over ServerAdminPassword
	ServerAdminPasswordExistingSecret string `json:"serverAdminPasswordExistingSecret,omitempty"`

	// PersistentVolumeClaim for server internal state
	// If not provided, server will use emptyDir (data lost on restart)
	PersistentVolumeClaim string `json:"persistentVolumeClaim,omitempty"`

	// Additional command-line arguments for kopia server start
	ExtraArgs []string `json:"extraArgs,omitempty"`
}

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

// KopiaRepositoryStorageSFTPSpec defines SFTP storage options for Kopia repository
type KopiaRepositoryStorageSFTPSpec struct {
	// Path to the repository on the SFTP server
	Path string `json:"path"`

	// SFTP server hostname
	Host string `json:"host"`

	// SFTP server port
	// +kubebuilder:default:=22
	Port int `json:"port,omitempty"`

	// Known hosts data for SSH host key verification
	KnownHostsData string `json:"knownHostsData,omitempty"`

	// Use external SSH command instead of built-in SSH
	// +kubebuilder:default:=false
	ExternalSSH bool `json:"externalSSH,omitempty"`

	// SSH command to use when ExternalSSH is true
	// +kubebuilder:default:="ssh"
	SSHCommand string `json:"sshCommand,omitempty"`

	// Directory shards configuration
	DirShards []int `json:"dirShards,omitempty"`

	// Secret containing SFTP credentials
	// Expected keys: username, password (optional), keyData (optional - SSH private key)
	// At least one of password or keyData must be provided
	CredentialsSecret string `json:"credentialsSecret"`
}

// KopiaRepositoryCachingSpec defines the desired state of KopiaRepositoryCaching
type KopiaRepositoryCachingSpec struct {
	// +kubebuilder:default:="cache"
	CacheDirectory string `json:"cacheDirectory,omitempty"`
	// +kubebuilder:default:=5242880000
	ContentCacheSizeBytes      int64 `json:"maxCacheSize,omitempty"`
	ContentCacheSizeLimitBytes int64 `json:"contentCacheSizeLimitBytes,omitempty"`
	// +kubebuilder:default:=5242880000
	MetadataCacheSizeBytes      int64 `json:"maxMetadataCacheSize,omitempty"`
	MetadataCacheSizeLimitBytes int64 `json:"metadataCacheSizeLimitBytes,omitempty"`
	// +kubebuilder:default:=30
	MaxListCacheDuration int64 `json:"maxListCacheDuration,omitempty"`
	MinMetadataSweepAge  int64 `json:"minMetadataSweepAge,omitempty"`
	MinContentSweepAge   int64 `json:"minContentSweepAge,omitempty"`
	MinIndexSweepAge     int64 `json:"minIndexSweepAge,omitempty"`
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
	// Storage type (currently only filesystem and sftp are supported)
	StorageType string `json:"storageType"`

	// Make the repository read-only
	ReadOnly bool `json:"readonly,omitempty"`
	// Allow loading from cache even if it's stale
	PermissiveCacheLoading bool `json:"permissiveCacheLoading,omitempty"`
	// Human-readable description of the repository to use in the UI.
	// +kubebuilder:default:=Cluster
	Description string `json:"description,omitempty"`
	// Enables Kopia actions in the repository.
	EnableActions bool `json:"enableActions"`

	// Cronjob for default schedule if not set in KopiaBackup
	// TODO: validate cron format
	DefaultSchedule string `json:"defaultSchedule,omitempty"`

	// Password for Kopia repository, ignored if RepositoryPasswordExistingSecret is set
	RepositoryPassword string `json:"repositoryPassword,omitempty"`
	// Secret name containing the password for the Kopia repository (must be in the same namespace); the password should be in KOPIA_PASSWORD key
	RepositoryPasswordExistingSecret string `json:"repositoryPasswordExistingSecret,omitempty"`

	// +kubebuilder:default:=900000000000
	FormatBlobCacheDuration int64 `json:"formatBlobCacheDuration,omitempty"`

	// Caching options for the repository.
	// +kubebuilder:default:={}
	Caching KopiaRepositoryCachingSpec `json:"caching,omitempty"`

	FileSystemOptions KopiaRepositoryStorageFileSystemSpec `json:"fileSystemOptions,omitempty"`

	SFTPOptions KopiaRepositoryStorageSFTPSpec `json:"sftpOptions,omitempty"`

	// Server configuration for running Kopia in server mode
	// When enabled, a centralized Kopia Server will be deployed
	Server KopiaServerSpec `json:"server,omitempty"`
}

// KopiaRepositoryStatus defines the observed state of KopiaRepository
type KopiaRepositoryStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Server status (when server mode is enabled)
	ServerReady bool `json:"serverReady,omitempty"`

	// URL to connect to the Kopia Server
	ServerURL string `json:"serverURL,omitempty"`

	// Deployment name of the Kopia Server
	ServerDeployment string `json:"serverDeployment,omitempty"`

	// Service name for the Kopia Server
	ServerService string `json:"serverService,omitempty"`

	// Conditions represent the latest available observations of the repository's state
	Conditions []metav1.Condition `json:"conditions,omitempty"`
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
