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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func TestBuildBackupCommand_DirectMode(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: false,
			},
		},
	}

	mountPath := "/data/default/test-pvc"
	result := buildBackupCommand(backup, repo, mountPath)

	// Verify direct mode command
	assert.Contains(t, result, "kopia snap create "+mountPath)
	assert.Contains(t, result, "kopia snap list "+mountPath)
	assert.Contains(t, result, "kopia content stats")
	assert.Contains(t, result, "kopia maintenance info")
	assert.NotContains(t, result, "kopia repository connect server")
}

func TestBuildBackupCommand_ServerMode(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
	}

	mountPath := "/data/default/test-pvc"
	result := buildBackupCommand(backup, repo, mountPath)

	// Verify server mode command
	assert.Contains(t, result, "kopia repository connect server")
	assert.Contains(t, result, "https://kopia-server-test-repo.default.svc.cluster.local:51515")
	assert.Contains(t, result, "kopia snap create "+mountPath)
	assert.Contains(t, result, "kopia snap list "+mountPath)
	assert.Contains(t, result, "kopia repo disconnect")
}

func TestConstructCronJob_BasicConfiguration(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
			Suspend:    false,
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
			Caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory: "/cache",
			},
		},
	}

	cronJobName := "snapshot-test-pvc"
	nodeName := "worker-1"
	appName := "my-app"

	cronJob := constructCronJob(backup, cronJobName, nodeName, appName, repo)

	require.NotNil(t, cronJob)

	// Verify basic CronJob properties
	assert.Equal(t, cronJobName, cronJob.Name)
	assert.Equal(t, "default", cronJob.Namespace)
	assert.Equal(t, "0 2 * * *", cronJob.Spec.Schedule)
	assert.Equal(t, batchv1.ForbidConcurrent, cronJob.Spec.ConcurrencyPolicy)

	// Verify suspend setting
	require.NotNil(t, cronJob.Spec.Suspend)
	assert.False(t, *cronJob.Spec.Suspend)

	// Verify history limits
	require.NotNil(t, cronJob.Spec.SuccessfulJobsHistoryLimit)
	assert.Equal(t, int32(3), *cronJob.Spec.SuccessfulJobsHistoryLimit)
	require.NotNil(t, cronJob.Spec.FailedJobsHistoryLimit)
	assert.Equal(t, int32(3), *cronJob.Spec.FailedJobsHistoryLimit)

	// Verify labels
	labels := cronJob.Spec.JobTemplate.Spec.Template.ObjectMeta.Labels
	assert.Equal(t, "test-pvc", labels["backup.cloudinfra.be/pvc-name"])
	assert.Equal(t, nodeName, labels["backup.cloudinfra.be/node-name"])
	assert.Equal(t, appName, labels["app.kubernetes.io/name"])
	assert.Equal(t, "false", labels["sidecar.istio.io/inject"])

	// Verify node affinity
	require.NotNil(t, cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity)
	require.NotNil(t, cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity.NodeAffinity)
	nodeAffinity := cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.NotNil(t, nodeAffinity)
	assert.Len(t, nodeAffinity.NodeSelectorTerms, 1)
	assert.Len(t, nodeAffinity.NodeSelectorTerms[0].MatchExpressions, 1)
	assert.Equal(t, "kubernetes.io/hostname", nodeAffinity.NodeSelectorTerms[0].MatchExpressions[0].Key)
	assert.Equal(t, corev1.NodeSelectorOpIn, nodeAffinity.NodeSelectorTerms[0].MatchExpressions[0].Operator)
	assert.Contains(t, nodeAffinity.NodeSelectorTerms[0].MatchExpressions[0].Values, nodeName)

	// Verify init containers
	assert.Len(t, cronJob.Spec.JobTemplate.Spec.Template.Spec.InitContainers, 1)
	assert.Equal(t, "wait", cronJob.Spec.JobTemplate.Spec.Template.Spec.InitContainers[0].Name)

	// Verify main container
	assert.Len(t, cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers, 1)
	container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "snapshot", container.Name)

	// Verify restart policy
	assert.Equal(t, corev1.RestartPolicyOnFailure, cronJob.Spec.JobTemplate.Spec.Template.Spec.RestartPolicy)
}

func TestConstructCronJob_SuspendTrue(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
			Suspend:    true,
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
		},
	}

	cronJob := constructCronJob(backup, "snapshot-test-pvc", "worker-1", "my-app", repo)

	require.NotNil(t, cronJob)
	require.NotNil(t, cronJob.Spec.Suspend)
	assert.True(t, *cronJob.Spec.Suspend)

	// Also verify job template suspend is set
	require.NotNil(t, cronJob.Spec.JobTemplate.Spec.Suspend)
	assert.True(t, *cronJob.Spec.JobTemplate.Spec.Suspend)
}

func TestConstructCronJob_ServerModeConfig(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
		Status: backupv1alpha1.KopiaRepositoryStatus{
			TLSCertFingerprint: "ABC123",
		},
	}

	cronJob := constructCronJob(backup, "snapshot-test-pvc", "worker-1", "my-app", repo)

	require.NotNil(t, cronJob)

	container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]

	// Verify environment variables for server mode
	var hasTLSFingerprint bool
	var hasKopiaPassword bool
	for _, env := range container.Env {
		if env.Name == "KOPIA_TLS_FINGERPRINT" && env.Value == "ABC123" {
			hasTLSFingerprint = true
		}
		if env.Name == "KOPIA_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			hasKopiaPassword = true
		}
	}
	assert.True(t, hasTLSFingerprint, "Expected KOPIA_TLS_FINGERPRINT env var")
	assert.True(t, hasKopiaPassword, "Expected KOPIA_PASSWORD env var from secret")

	// Verify envFrom includes the credentials secret
	var hasCredentialsSecret bool
	for _, envFrom := range container.EnvFrom {
		if envFrom.SecretRef != nil && strings.Contains(envFrom.SecretRef.Name, "kopia-backup-user") {
			hasCredentialsSecret = true
		}
	}
	assert.True(t, hasCredentialsSecret, "Expected credentials secret in envFrom")
}

func TestConstructCronJob_SFTPStorage(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "sftp",
			SFTPOptions: backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
				Host:              "sftp.example.com",
				Port:              22,
				Path:              "/backup",
				CredentialsSecret: "sftp-creds",
			},
			Caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory: "/cache",
			},
		},
	}

	cronJob := constructCronJob(backup, "snapshot-test-pvc", "worker-1", "my-app", repo)

	require.NotNil(t, cronJob)

	// Verify SFTP credentials volume is mounted
	var hasSFTPCredsVolume bool
	for _, vol := range cronJob.Spec.JobTemplate.Spec.Template.Spec.Volumes {
		if vol.Name == "sftp-credentials" && vol.Secret != nil {
			hasSFTPCredsVolume = true
			assert.Equal(t, "sftp-creds", vol.Secret.SecretName)
		}
	}
	assert.True(t, hasSFTPCredsVolume, "Expected sftp-credentials volume")

	container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	var hasSFTPCredsMount bool
	for _, mount := range container.VolumeMounts {
		if mount.Name == "sftp-credentials" {
			hasSFTPCredsMount = true
			assert.Equal(t, "/sftp-creds", mount.MountPath)
			assert.True(t, mount.ReadOnly)
		}
	}
	assert.True(t, hasSFTPCredsMount, "Expected sftp-credentials volume mount")
}

func TestConstructConfigMap(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			Description: "Test Repository",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path: "/backup/repo",
			},
			Caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:         "/cache",
				ContentCacheSizeBytes:  5242880000,
				MetadataCacheSizeBytes: 5242880000,
				MaxListCacheDuration:   30,
			},
			FormatBlobCacheDuration: 900000000000,
			EnableActions:           true,
		},
	}

	configMap := constructConfigMap(backup, repo)

	require.NotNil(t, configMap)
	assert.Equal(t, "kopia-config-test-repo", configMap.Name)
	assert.Equal(t, "default", configMap.Namespace)

	// Verify labels
	assert.Equal(t, "test-pvc", configMap.Labels["backup.cloudinfra.be/pvc-name"])

	// Verify config data exists
	require.Contains(t, configMap.Data, "repository.config")
	configData := configMap.Data["repository.config"]

	// Verify config content
	assert.Contains(t, configData, `"type": "filesystem"`)
	assert.Contains(t, configData, `"path": "/backup/repo"`)
	assert.Contains(t, configData, `"hostname": "test-host"`)
	assert.Contains(t, configData, `"username": "test-user"`)
	assert.Contains(t, configData, `"description": "Test Repository"`)
	assert.Contains(t, configData, `"enableActions": true`)
}

func TestBuildServerModeConfig(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName: "test-pvc",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Status: backupv1alpha1.KopiaRepositoryStatus{
			TLSCertFingerprint: "ABC123DEF456",
		},
	}

	envVars := []corev1.EnvVar{{Name: "EXISTING", Value: "value"}}
	envFrom := []corev1.EnvFromSource{}
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	resultEnvVars, resultEnvFrom, resultVolumeMounts, resultVolumes := buildServerModeConfig(
		backup, repo, envVars, envFrom, volumeMounts, volumes,
	)

	// Verify env vars added
	assert.Greater(t, len(resultEnvVars), len(envVars))

	// Verify KOPIA_PASSWORD is added with secret reference
	var hasPassword bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			hasPassword = true
			assert.Contains(t, env.ValueFrom.SecretKeyRef.Name, "kopia-backup-user")
		}
	}
	assert.True(t, hasPassword, "Expected KOPIA_PASSWORD with secret ref")

	// Verify TLS fingerprint is added
	var hasTLS bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_TLS_FINGERPRINT" && env.Value == "ABC123DEF456" {
			hasTLS = true
		}
	}
	assert.True(t, hasTLS, "Expected KOPIA_TLS_FINGERPRINT")

	// Verify credentials secret is added to envFrom
	assert.Greater(t, len(resultEnvFrom), 0)
	var hasCredentialsEnvFrom bool
	for _, ef := range resultEnvFrom {
		if ef.SecretRef != nil && strings.Contains(ef.SecretRef.Name, "kopia-backup-user") {
			hasCredentialsEnvFrom = true
		}
	}
	assert.True(t, hasCredentialsEnvFrom, "Expected credentials secret in envFrom")

	// Verify cache volume is added
	var hasCacheVolume bool
	for _, vol := range resultVolumes {
		if vol.Name == "kopia-cache" && vol.EmptyDir != nil {
			hasCacheVolume = true
		}
	}
	assert.True(t, hasCacheVolume, "Expected kopia-cache volume")

	// Verify cache volume mount
	var hasCacheMount bool
	for _, mount := range resultVolumeMounts {
		if mount.Name == "kopia-cache" && mount.MountPath == "/cache" {
			hasCacheMount = true
		}
	}
	assert.True(t, hasCacheMount, "Expected kopia-cache volume mount")
}

func TestBuildDirectModeConfig_Filesystem(t *testing.T) {
	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			StorageType:                      "filesystem",
			RepositoryPassword:               "test-password",
			RepositoryPasswordExistingSecret: "",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
		},
	}

	envVars := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}
	cacheDir := "/cache"

	resultEnvVars, _, resultVolumeMounts, resultVolumes := buildDirectModeConfig(
		repo, cacheDir, envVars, envFrom, volumeMounts, volumes,
	)

	// Verify cache directory env var
	var hasCacheDir bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_CACHE_DIRECTORY" && env.Value == cacheDir {
			hasCacheDir = true
		}
	}
	assert.True(t, hasCacheDir, "Expected KOPIA_CACHE_DIRECTORY")

	// Verify password env var (direct value since no existing secret)
	var hasPassword bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_PASSWORD" && env.Value == "test-password" {
			hasPassword = true
		}
	}
	assert.True(t, hasPassword, "Expected KOPIA_PASSWORD with direct value")

	// Verify NFS volume
	var hasNFSVolume bool
	for _, vol := range resultVolumes {
		if vol.Name == "repo" && vol.NFS != nil {
			hasNFSVolume = true
			assert.Equal(t, "nfs.example.com", vol.NFS.Server)
			assert.Equal(t, "/exports/backup", vol.NFS.Path)
		}
	}
	assert.True(t, hasNFSVolume, "Expected NFS volume")

	// Verify repo volume mount
	var hasRepoMount bool
	for _, mount := range resultVolumeMounts {
		if mount.Name == "repo" && mount.MountPath == "/backup" {
			hasRepoMount = true
		}
	}
	assert.True(t, hasRepoMount, "Expected repo volume mount")

	// Verify config volume mount
	var hasConfigMount bool
	for _, mount := range resultVolumeMounts {
		if mount.Name == "config" {
			hasConfigMount = true
		}
	}
	assert.True(t, hasConfigMount, "Expected config volume mount")
}

func TestBuildDirectModeConfig_SFTP(t *testing.T) {
	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			StorageType:                      "sftp",
			RepositoryPasswordExistingSecret: "existing-password-secret",
			SFTPOptions: backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
				Host:              "sftp.example.com",
				Port:              22,
				Path:              "/backup",
				CredentialsSecret: "sftp-creds",
			},
		},
	}

	envVars := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}
	cacheDir := "/cache"

	_, resultEnvFrom, resultVolumeMounts, resultVolumes := buildDirectModeConfig(
		repo, cacheDir, envVars, envFrom, volumeMounts, volumes,
	)

	// Verify password from existing secret via envFrom
	var hasExistingSecretEnvFrom bool
	for _, ef := range resultEnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "existing-password-secret" {
			hasExistingSecretEnvFrom = true
		}
	}
	assert.True(t, hasExistingSecretEnvFrom, "Expected existing password secret in envFrom")

	// Verify SFTP credentials volume
	var hasSFTPCredsVolume bool
	for _, vol := range resultVolumes {
		if vol.Name == "sftp-credentials" && vol.Secret != nil {
			hasSFTPCredsVolume = true
			assert.Equal(t, "sftp-creds", vol.Secret.SecretName)
		}
	}
	assert.True(t, hasSFTPCredsVolume, "Expected sftp-credentials volume")

	// Verify SFTP credentials mount
	var hasSFTPCredsMount bool
	for _, mount := range resultVolumeMounts {
		if mount.Name == "sftp-credentials" {
			hasSFTPCredsMount = true
			assert.True(t, mount.ReadOnly)
		}
	}
	assert.True(t, hasSFTPCredsMount, "Expected sftp-credentials volume mount")

	// Verify kopia-cache volume for SFTP
	var hasCacheVolume bool
	for _, vol := range resultVolumes {
		if vol.Name == "kopia-cache" && vol.EmptyDir != nil {
			hasCacheVolume = true
		}
	}
	assert.True(t, hasCacheVolume, "Expected kopia-cache volume for SFTP")
}

func TestInt32Ptr(t *testing.T) {
	tests := []struct {
		input    int32
		expected int32
	}{
		{0, 0},
		{1, 1},
		{-1, -1},
		{100, 100},
	}

	for _, tt := range tests {
		result := int32Ptr(tt.input)
		require.NotNil(t, result)
		assert.Equal(t, tt.expected, *result)
	}
}

func TestConstructCronJob_MountPathWithAppName(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "myns",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
		},
	}

	// With app name
	cronJob := constructCronJob(backup, "snapshot-test-pvc", "worker-1", "my-app", repo)
	require.NotNil(t, cronJob)

	container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	command := strings.Join(container.Args, " ")
	assert.Contains(t, command, "/data/myns/my-app/test-pvc")
}

func TestConstructCronJob_MountPathWithoutAppName(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "myns",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
		},
	}

	// Without app name
	cronJob := constructCronJob(backup, "snapshot-test-pvc", "worker-1", "", repo)
	require.NotNil(t, cronJob)

	container := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	command := strings.Join(container.Args, " ")
	assert.Contains(t, command, "/data/myns/test-pvc")
	assert.NotContains(t, command, "/data/myns//test-pvc")
}

func TestBuildDirectModeConfig_FilesystemWithNFS(t *testing.T) {
	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			StorageType:        "filesystem",
			RepositoryPassword: "test-password",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/mnt/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
		},
	}

	envVars := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}
	cacheDir := "/cache"

	resultEnvVars, _, resultVolumeMounts, resultVolumes := buildDirectModeConfig(
		repo, cacheDir, envVars, envFrom, volumeMounts, volumes,
	)

	// Verify cache directory env var is set
	var hasCacheDir bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_CACHE_DIRECTORY" && env.Value == cacheDir {
			hasCacheDir = true
		}
	}
	assert.True(t, hasCacheDir, "Expected KOPIA_CACHE_DIRECTORY env var")

	// Verify NFS volume is created
	var hasNFSVolume bool
	for _, vol := range resultVolumes {
		if vol.Name == "repo" && vol.NFS != nil {
			hasNFSVolume = true
			assert.Equal(t, "nfs.example.com", vol.NFS.Server)
			assert.Equal(t, "/exports/backup", vol.NFS.Path)
		}
	}
	assert.True(t, hasNFSVolume, "Expected NFS volume")

	// Verify repo mount
	var hasRepoMount bool
	for _, mount := range resultVolumeMounts {
		if mount.Name == "repo" && mount.MountPath == "/mnt/backup" {
			hasRepoMount = true
		}
	}
	assert.True(t, hasRepoMount, "Expected repo volume mount at /mnt/backup")
}

func TestConstructConfigMap_WithStorageTypeAndPath(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path: "/backup/repo",
			},
			Caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:         "/cache",
				ContentCacheSizeBytes:  1024,
				MetadataCacheSizeBytes: 512,
			},
		},
	}

	configMap := constructConfigMap(backup, repo)

	require.NotNil(t, configMap)
	require.Contains(t, configMap.Data, "repository.config")

	configData := configMap.Data["repository.config"]
	assert.Contains(t, configData, `"type": "filesystem"`)
	assert.Contains(t, configData, `"path": "/backup/repo"`)
	assert.Contains(t, configData, `"hostname": "test-host"`)
	assert.Contains(t, configData, `"username": "test-user"`)
	assert.Contains(t, configData, `"cacheDirectory": "/cache"`)
}

func TestConstructCronJob_WithNodeNameEmpty(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Schedule:   "0 2 * * *",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
		},
	}

	// Empty node name - should still create CronJob but with empty affinity value
	cronJob := constructCronJob(backup, "snapshot-test-pvc", "", "my-app", repo)

	require.NotNil(t, cronJob)

	// Verify node affinity is still present (with empty value)
	require.NotNil(t, cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity)
	nodeAffinity := cronJob.Spec.JobTemplate.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	require.NotNil(t, nodeAffinity)
}

func TestBuildBackupCommand_WithMountPath(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName:    "test-pvc",
			Repository: "test-repo",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: false,
			},
		},
	}

	// Test with specific mount path
	mountPath := "/custom/path/to/pvc"
	result := buildBackupCommand(backup, repo, mountPath)

	assert.Contains(t, result, "kopia snap create "+mountPath)
	assert.Contains(t, result, "kopia snap list "+mountPath)
}

func TestBuildServerModeConfig_WithRepositoryPasswordSecret(t *testing.T) {
	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaBackupSpec{
			PVCName: "test-pvc",
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			RepositoryPasswordExistingSecret: "my-custom-password-secret",
		},
		Status: backupv1alpha1.KopiaRepositoryStatus{
			TLSCertFingerprint: "FINGERPRINT123",
		},
	}

	envVars := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	volumeMounts := []corev1.VolumeMount{}
	volumes := []corev1.Volume{}

	resultEnvVars, _, _, _ := buildServerModeConfig(
		backup, repo, envVars, envFrom, volumeMounts, volumes,
	)

	// Verify KOPIA_PASSWORD references the custom secret
	var hasCustomPassword bool
	for _, env := range resultEnvVars {
		if env.Name == "KOPIA_PASSWORD" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			// Should still use the user credentials secret, not the repository secret
			if strings.Contains(env.ValueFrom.SecretKeyRef.Name, "kopia-backup-user") {
				hasCustomPassword = true
			}
		}
	}
	assert.True(t, hasCustomPassword, "Expected KOPIA_PASSWORD from user credentials secret")
}
