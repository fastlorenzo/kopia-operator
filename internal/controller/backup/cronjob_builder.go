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
	"encoding/json"
	"fmt"
	"path/filepath"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// buildBackupCommand builds the shell command to run in the backup container.
func buildBackupCommand(backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository, mountPath string) string {
	if repo.Spec.Server.Enabled {
		serverURL := fmt.Sprintf("https://kopia-server-%s.%s.svc.cluster.local:%d",
			repo.Name,
			repo.Namespace,
			repo.Spec.Server.Exposure.ServicePort)

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

	return "" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[01/04] Create snapshot ...\"          && kopia snap create " + mountPath + "\n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[02/04] List snapshots ...\"           && kopia snap list " + mountPath + "\n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[03/04] Show stats ...\"               && kopia content stats \n" +
		"printf \"\\e[1;32m%-6s\\e[m\\n\" \"[04/04] Show maintenance info ...\"      && kopia maintenance info \n"
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

	initContainers := []corev1.Container{
		{
			Name:    "wait",
			Image:   kopiaImage,
			Command: []string{"/scripts/sleep.sh"},
			Args:    []string{"1", "10"},
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
			SuccessfulJobsHistoryLimit: int32Ptr(maxBackupHistoryEntries),
			FailedJobsHistoryLimit:     int32Ptr(maxBackupHistoryEntries),
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
									Image:        kopiaImage,
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

// buildServerModeConfig configures env vars, volumes and mounts for server mode.
func buildServerModeConfig(
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
	envVars []corev1.EnvVar,
	envFrom []corev1.EnvFromSource,
	volumeMounts []corev1.VolumeMount,
	volumes []corev1.Volume,
) ([]corev1.EnvVar, []corev1.EnvFromSource, []corev1.VolumeMount, []corev1.Volume) {
	secretName := fmt.Sprintf("kopia-backup-user-%s-%s", backup.Namespace, backup.Spec.PVCName)

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
							Name: fmt.Sprintf("kopia-config-%s", repo.Name),
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
						DefaultMode: int32Ptr(0600),
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
	Type   string                 `json:"type"`
	Config map[string]interface{} `json:"config"`
}

type kopiaConfigCaching struct {
	CacheDirectory       string `json:"cacheDirectory"`
	MaxCacheSize         int64  `json:"maxCacheSize"`
	MaxMetadataCacheSize int64  `json:"maxMetadataCacheSize"`
	MaxListCacheDuration int64  `json:"maxListCacheDuration"`
}

// buildConfigMap builds the Kopia repository.config ConfigMap for direct mode.
func buildConfigMap(backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) (*corev1.ConfigMap, error) {
	storageConfig := map[string]interface{}{
		"path":      repo.Spec.FileSystemOptions.Path,
		"dirShards": nil,
	}

	cfg := kopiaConfigData{
		Storage: kopiaConfigStorage{
			Type:   string(repo.Spec.StorageType),
			Config: storageConfig,
		},
		Caching: kopiaConfigCaching{
			CacheDirectory:       repo.Spec.Caching.CacheDirectory,
			MaxCacheSize:         repo.Spec.Caching.ContentCacheSizeBytes,
			MaxMetadataCacheSize: repo.Spec.Caching.MetadataCacheSizeBytes,
			MaxListCacheDuration: repo.Spec.Caching.MaxListCacheDuration,
		},
		Hostname:           repo.Spec.Hostname,
		Username:           repo.Spec.Username,
		Description:        repo.Spec.Description,
		EnableActions:      repo.Spec.EnableActions,
		FormatBlobCacheDur: repo.Spec.FormatBlobCacheDuration,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal repository config: %w", err)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("kopia-config-%s", repo.Name),
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

// int32Ptr returns a pointer to an int32.
func int32Ptr(i int32) *int32 {
	return &i
}
