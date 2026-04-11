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
	"encoding/json"
	"fmt"
	"path/filepath"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

const (
	// maxBackupHistoryEntries is the maximum number of backup history entries to keep.
	maxBackupHistoryEntries = 10
)

// buildBackupCommand builds the shell command to run in the backup container.
func buildBackupCommand(_ *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository, mountPath string) string {
	if repo.Spec.Server.Enabled {
		serverURL := fmt.Sprintf("https://kopia-server-%s.%s.svc.cluster.local:%d",
			repo.Name, repo.Namespace, repo.Spec.Server.Exposure.ServicePort)

		return fmt.Sprintf(`set -e
echo "[1/4] Connecting to Kopia Server..."
kopia repository connect server \
  --url=%s \
  --server-cert-fingerprint="${KOPIA_TLS_FINGERPRINT}" \
  --override-username="${KOPIA_SERVER_USERNAME%%%%@*}" \
  --override-hostname="${KOPIA_SERVER_USERNAME#*@}"

echo "[2/4] Creating snapshot..."
kopia snapshot create %s

echo "[3/4] Listing snapshots..."
kopia snapshot list %s

echo "[4/4] Disconnecting repository..."
kopia repository disconnect
`, serverURL, mountPath, mountPath)
	}

	return fmt.Sprintf(`set -e
echo "[1/4] Creating snapshot..."
kopia snapshot create %s

echo "[2/4] Listing snapshots..."
kopia snapshot list %s

echo "[3/4] Showing content stats..."
kopia content stats

echo "[4/4] Showing maintenance info..."
kopia maintenance info
`, mountPath, mountPath)
}

// buildCronJob constructs a CronJob for the backup.
func buildCronJob(
	backup *backupv1alpha1.KopiaBackup,
	cronJobName string,
	nodeName string,
	appName string,
	repo *backupv1alpha1.KopiaRepository,
	kopiaImage string,
) *batchv1.CronJob {
	var mountPath string
	if appName != "" {
		mountPath = "/data/" + backup.Namespace + "/" + appName + "/" + backup.Spec.PVCName
	} else {
		mountPath = "/data/" + backup.Namespace + "/" + backup.Spec.PVCName
	}

	kopiaCacheDirectory := repo.Spec.Caching.CacheDirectory
	kopiaLogDir := filepath.Join(repo.Spec.FileSystemOptions.Path, ".kopia", "logs")

	envVars := []corev1.EnvVar{
		{Name: "KOPIA_LOG_DIR", Value: kopiaLogDir},
	}
	var envFrom []corev1.EnvFromSource

	volumeMounts := []corev1.VolumeMount{
		{Name: "data", MountPath: mountPath},
	}
	volumes := []corev1.Volume{
		{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: backup.Spec.PVCName,
				},
			},
		},
	}

	if repo.Spec.Server.Enabled {
		envVars, envFrom, volumeMounts, volumes = buildServerModeConfig(
			backup, repo, envVars, envFrom, volumeMounts, volumes,
		)
	} else {
		envVars, envFrom, volumeMounts, volumes = buildDirectModeConfig(
			repo, kopiaCacheDirectory, envVars, envFrom, volumeMounts, volumes,
		)
	}

	containerSecCtx := &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	initContainers := []corev1.Container{
		{
			Name:            "wait",
			Image:           kopiaImage,
			Command:         []string{"/scripts/sleep.sh"},
			Args:            []string{"1", "10"},
			SecurityContext: containerSecCtx,
		},
	}

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cronJobName,
			Namespace: backup.Namespace,
			Labels: map[string]string{
				"backup.cloudinfra.be/backup":     backup.Name,
				"backup.cloudinfra.be/repository": backup.Spec.Repository,
				"backup.cloudinfra.be/pvc-name":   backup.Spec.PVCName,
			},
		},
		Spec: batchv1.CronJobSpec{
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			Schedule:                   backup.Spec.Schedule,
			Suspend:                    &backup.Spec.Suspend,
			SuccessfulJobsHistoryLimit: ptr.To(int32(maxBackupHistoryEntries)),
			FailedJobsHistoryLimit:     ptr.To(int32(maxBackupHistoryEntries)),
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"backup.cloudinfra.be/backup":     backup.Name,
						"backup.cloudinfra.be/repository": backup.Spec.Repository,
						"backup.cloudinfra.be/pvc-name":   backup.Spec.PVCName,
					},
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"backup.cloudinfra.be/backup":     backup.Name,
								"backup.cloudinfra.be/repository": backup.Spec.Repository,
								"backup.cloudinfra.be/pvc-name":   backup.Spec.PVCName,
								"backup.cloudinfra.be/node-name":  nodeName,
								"app.kubernetes.io/name":          appName,
								"sidecar.istio.io/inject":         "false",
							},
						},
						Spec: corev1.PodSpec{
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: ptr.To(true),
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
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
									Name:            "snapshot",
									Image:           kopiaImage,
									Args:            []string{"/bin/bash", "-c", buildBackupCommand(backup, repo, mountPath)},
									Env:             envVars,
									EnvFrom:         envFrom,
									VolumeMounts:    volumeMounts,
									SecurityContext: containerSecCtx,
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

// buildServerModeConfig configures env vars, volumes and mounts for server mode.
func buildServerModeConfig(
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
	envVars []corev1.EnvVar,
	envFrom []corev1.EnvFromSource,
	volumeMounts []corev1.VolumeMount,
	volumes []corev1.Volume,
) ([]corev1.EnvVar, []corev1.EnvFromSource, []corev1.VolumeMount, []corev1.Volume) {
	secretName := naming.UserSecretName(backup.Namespace, backup.Spec.PVCName)

	envFrom = append(envFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
		},
	})

	envVars = append(envVars, corev1.EnvVar{
		Name: "KOPIA_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  "KOPIA_SERVER_PASSWORD",
			},
		},
	})

	if repo.Status.TLSCertFingerprint != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "KOPIA_TLS_FINGERPRINT",
			Value: repo.Status.TLSCertFingerprint,
		})
	}

	volumes = append(volumes, corev1.Volume{
		Name: "kopia-cache",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: resource.NewQuantity(3<<30, resource.BinarySI),
			},
		},
	})

	volumeMounts = append(volumeMounts, corev1.VolumeMount{
		Name:      "kopia-cache",
		MountPath: "/cache",
	})

	envVars = append(envVars, corev1.EnvVar{
		Name:  "KOPIA_CACHE_DIRECTORY",
		Value: "/cache/kopia",
	})

	return envVars, envFrom, volumeMounts, volumes
}

// buildDirectModeConfig configures env vars, volumes and mounts for direct storage access.
func buildDirectModeConfig(
	repo *backupv1alpha1.KopiaRepository,
	kopiaCacheDirectory string,
	envVars []corev1.EnvVar,
	envFrom []corev1.EnvFromSource,
	volumeMounts []corev1.VolumeMount,
	volumes []corev1.Volume,
) ([]corev1.EnvVar, []corev1.EnvFromSource, []corev1.VolumeMount, []corev1.Volume) {
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
	case backupv1alpha1.StorageTypeFilesystem:
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "repo",
			MountPath: repo.Spec.FileSystemOptions.Path,
		})

		volumes = append(volumes,
			corev1.Volume{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: naming.ConfigMapName(repo.Name),
						},
					},
				},
			},
			corev1.Volume{
				Name: "repo",
				VolumeSource: corev1.VolumeSource{
					NFS: &corev1.NFSVolumeSource{
						Server: repo.Spec.FileSystemOptions.NFSServer,
						Path:   repo.Spec.FileSystemOptions.NFSPath,
					},
				},
			},
		)

	case backupv1alpha1.StorageTypeSFTP:
		volumes = append(volumes,
			corev1.Volume{
				Name: "sftp-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  repo.Spec.SFTPOptions.CredentialsSecret,
						DefaultMode: ptr.To(int32(0600)),
					},
				},
			},
			corev1.Volume{
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			corev1.Volume{
				Name: "kopia-cache",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{
						SizeLimit: resource.NewQuantity(3<<30, resource.BinarySI),
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

	// Repository password from Secret.
	envFrom = append(envFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: repo.Spec.PasswordSecretName,
			},
		},
	})

	return envVars, envFrom, volumeMounts, volumes
}

// kopiaConfigData is the JSON structure for a Kopia repository.config file.
type kopiaConfigData struct {
	Storage            kopiaConfigStorage `json:"storage"`
	Caching            kopiaConfigCaching `json:"caching"`
	Hostname           string             `json:"hostname"`
	Username           string             `json:"username"`
	Description        string             `json:"description"`
	EnableActions      bool               `json:"enableActions"`
	FormatBlobCacheDur int64              `json:"formatBlobCacheDuration"`
}

type kopiaConfigStorage struct {
	Type   string `json:"type"`
	Config any    `json:"config"`
}

type kopiaConfigCaching struct {
	CacheDirectory       string `json:"cacheDirectory"`
	MaxCacheSize         int64  `json:"maxCacheSize"`
	MaxMetadataCacheSize int64  `json:"maxMetadataCacheSize"`
	MaxListCacheDuration int64  `json:"maxListCacheDuration"`
}

// filesystemStorageConfig is the Kopia storage config for filesystem backends.
type filesystemStorageConfig struct {
	Path      string `json:"path"`
	DirShards *[]int `json:"dirShards"`
}

// sftpStorageConfig is the Kopia storage config for SFTP backends.
type sftpStorageConfig struct {
	Path           string `json:"path"`
	Host           string `json:"host"`
	Port           int32  `json:"port,omitempty"`
	KnownHostsData string `json:"knownHostsData,omitempty"`
	ExternalSSH    bool   `json:"externalSSH,omitempty"`
	SSHCommand     string `json:"sshCommand,omitempty"`
	DirShards      []int  `json:"dirShards,omitempty"`
}

// buildConfigMap builds the Kopia repository.config ConfigMap for direct mode.
func buildConfigMap(backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) (*corev1.ConfigMap, error) {
	var storageConfig any
	switch repo.Spec.StorageType {
	case backupv1alpha1.StorageTypeFilesystem:
		storageConfig = filesystemStorageConfig{
			Path: repo.Spec.FileSystemOptions.Path,
		}
	case backupv1alpha1.StorageTypeSFTP:
		storageConfig = sftpStorageConfig{
			Path:           repo.Spec.SFTPOptions.Path,
			Host:           repo.Spec.SFTPOptions.Host,
			Port:           repo.Spec.SFTPOptions.Port,
			KnownHostsData: repo.Spec.SFTPOptions.KnownHostsData,
			ExternalSSH:    repo.Spec.SFTPOptions.ExternalSSH,
			SSHCommand:     repo.Spec.SFTPOptions.SSHCommand,
			DirShards:      repo.Spec.SFTPOptions.DirShards,
		}
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", repo.Spec.StorageType)
	}

	cfg := kopiaConfigData{
		Storage: kopiaConfigStorage{
			Type:   string(repo.Spec.StorageType),
			Config: storageConfig,
		},
		Caching: kopiaConfigCaching{
			CacheDirectory:       repo.Spec.Caching.CacheDirectory,
			MaxCacheSize:         repo.Spec.Caching.ContentCacheSize.Value(),
			MaxMetadataCacheSize: repo.Spec.Caching.MetadataCacheSize.Value(),
			MaxListCacheDuration: repo.Spec.Caching.MaxListCacheDuration,
		},
		Hostname:           repo.Spec.Hostname,
		Username:           repo.Spec.Username,
		Description:        repo.Spec.Description,
		EnableActions:      repo.Spec.EnableActions,
		FormatBlobCacheDur: repo.Spec.FormatBlobCacheDurationSeconds * 1e9, // convert seconds to nanoseconds
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repository config: %w", err)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      naming.ConfigMapName(repo.Name),
			Namespace: backup.Namespace,
			Labels: map[string]string{
				"backup.cloudinfra.be/pvc-name": backup.Spec.PVCName,
			},
		},
		Data: map[string]string{
			"repository.config": string(data),
		},
	}, nil
}
