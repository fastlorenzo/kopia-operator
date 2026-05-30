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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

var _ = Describe("CronJob Builder", func() {
	var (
		repo   *backupv1alpha1.KopiaRepository
		backup *backupv1alpha1.KopiaBackup
	)

	BeforeEach(func() {
		repo = &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-repo",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				Hostname:           "test-host",
				Username:           "test-user",
				StorageType:        backupv1alpha1.StorageTypeFilesystem,
				PasswordSecretName: "kopia-password",
				FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
					Path: "/backup/kopia",
				},
			},
		}
		backup = &backupv1alpha1.KopiaBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaBackupSpec{
				PVCName:    "my-pvc",
				Schedule:   "0 3 * * *",
				Repository: "test-repo",
			},
		}
	})

	Context("buildBackupCommand", func() {
		It("should build a direct mode command with snapshot create", func() {
			cmd := buildBackupCommand(backup, repo, "/data")
			Expect(cmd).To(ContainSubstring("kopia snapshot create /data"))
		})

		It("should build a server mode command when server is enabled", func() {
			repo.Spec.Server.Enabled = true
			backup.Status.Username = "user-test-backup"

			cmd := buildBackupCommand(backup, repo, "/data")
			Expect(cmd).To(ContainSubstring("kopia repository connect server"))
			Expect(cmd).To(ContainSubstring("kopia snapshot create /data"))
		})
	})

	Context("buildCronJob", func() {
		It("should create a valid CronJob for filesystem storage", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "kopia/kopia:latest")
			Expect(cj.Name).To(Equal("snapshot-my-pvc"))
			Expect(cj.Namespace).To(Equal("default"))
			Expect(cj.Spec.Schedule).To(Equal("0 3 * * *"))
			Expect(cj.Spec.Suspend).NotTo(BeNil())
			Expect(*cj.Spec.Suspend).To(BeFalse())

			containers := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
			Expect(containers).To(HaveLen(1))
			Expect(containers[0].Image).To(Equal("kopia/kopia:latest"))

			volumeNames := make([]string, 0)
			for _, v := range cj.Spec.JobTemplate.Spec.Template.Spec.Volumes {
				volumeNames = append(volumeNames, v.Name)
			}
			Expect(volumeNames).To(ContainElement("data"))
		})

		It("should set the image when kopiaImage is provided", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "custom/kopia:v1")
			containers := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
			Expect(containers[0].Image).To(Equal("custom/kopia:v1"))
		})

		It("should set suspend to true when backup is suspended", func() {
			backup.Spec.Suspend = true
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "")
			Expect(*cj.Spec.Suspend).To(BeTrue())
		})

		It("should include node affinity for the correct node", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "worker-node-1", "my-app", repo, "")
			affinity := cj.Spec.JobTemplate.Spec.Template.Spec.Affinity
			Expect(affinity).NotTo(BeNil())
			Expect(affinity.NodeAffinity).NotTo(BeNil())
		})

		It("should use pvc-only mount path when appName is empty", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "", repo, "kopia/kopia:latest")
			container := cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
			// With empty appName, mount path should be /data/<namespace>/<pvc> (no app segment)
			Expect(container.Args[2]).To(ContainSubstring("/data/default/my-pvc"))
			Expect(container.Args[2]).NotTo(ContainSubstring("/data/default//my-pvc"))
		})

		It("should set empty app label when appName is empty", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "", "", repo, "")
			labels := cj.Spec.JobTemplate.Spec.Template.ObjectMeta.Labels
			Expect(labels["app.kubernetes.io/name"]).To(Equal(""))
			Expect(labels["backup.cloudinfra.be/node-name"]).To(Equal(""))
		})

		It("should omit node affinity with empty nodeName", func() {
			cj := buildCronJob(backup, "snapshot-my-pvc", "", "my-app", repo, "")
			Expect(cj.Spec.JobTemplate.Spec.Template.Spec.Affinity).To(BeNil())
		})

		It("should default jobs history limits to 3 successful / 1 failed when unset", func() {
			backup.Spec.SuccessfulJobsHistoryLimit = nil
			backup.Spec.FailedJobsHistoryLimit = nil
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "")
			Expect(cj.Spec.SuccessfulJobsHistoryLimit).NotTo(BeNil())
			Expect(*cj.Spec.SuccessfulJobsHistoryLimit).To(Equal(int32(3)))
			Expect(cj.Spec.FailedJobsHistoryLimit).NotTo(BeNil())
			Expect(*cj.Spec.FailedJobsHistoryLimit).To(Equal(int32(1)))
		})

		It("should honor explicit jobs history limits from the spec", func() {
			backup.Spec.SuccessfulJobsHistoryLimit = ptr.To(int32(7))
			backup.Spec.FailedJobsHistoryLimit = ptr.To(int32(5))
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "")
			Expect(*cj.Spec.SuccessfulJobsHistoryLimit).To(Equal(int32(7)))
			Expect(*cj.Spec.FailedJobsHistoryLimit).To(Equal(int32(5)))
		})

		It("should honor an explicit zero jobs history limit", func() {
			backup.Spec.SuccessfulJobsHistoryLimit = ptr.To(int32(0))
			backup.Spec.FailedJobsHistoryLimit = ptr.To(int32(0))
			cj := buildCronJob(backup, "snapshot-my-pvc", "node-a", "my-app", repo, "")
			Expect(*cj.Spec.SuccessfulJobsHistoryLimit).To(Equal(int32(0)))
			Expect(*cj.Spec.FailedJobsHistoryLimit).To(Equal(int32(0)))
		})
	})

	Context("buildConfigMap", func() {
		It("should produce valid JSON for filesystem storage", func() {
			cm, err := buildConfigMap(backup, repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(cm.Name).To(Equal(naming.ConfigMapName("test-repo")))
			Expect(cm.Namespace).To(Equal("default"))
			Expect(cm.Data).To(HaveKey("repository.config"))

			var config map[string]interface{}
			err = json.Unmarshal([]byte(cm.Data["repository.config"]), &config)
			Expect(err).NotTo(HaveOccurred())

			storage, ok := config["storage"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(storage["type"]).To(Equal("filesystem"))

			storageConfig, ok := storage["config"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(storageConfig["path"]).To(Equal("/backup/kopia"))
		})

		It("should produce valid JSON for SFTP storage", func() {
			repo.Spec.StorageType = backupv1alpha1.StorageTypeSFTP
			repo.Spec.SFTPOptions = backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
				Host:              "sftp.example.com",
				Port:              22,
				Path:              "/backups/kopia",
				CredentialsSecret: "sftp-creds",
			}

			cm, err := buildConfigMap(backup, repo)
			Expect(err).NotTo(HaveOccurred())

			var config map[string]interface{}
			err = json.Unmarshal([]byte(cm.Data["repository.config"]), &config)
			Expect(err).NotTo(HaveOccurred())

			storage := config["storage"].(map[string]interface{})
			Expect(storage["type"]).To(Equal("sftp"))

			storageConfig := storage["config"].(map[string]interface{})
			Expect(storageConfig["path"]).To(Equal("/backups/kopia"))
			Expect(storageConfig["host"]).To(Equal("sftp.example.com"))
			Expect(storageConfig["port"]).To(BeNumerically("==", 22))
		})

		It("should include hostname and username", func() {
			cm, err := buildConfigMap(backup, repo)
			Expect(err).NotTo(HaveOccurred())

			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"hostname": "test-host"`))
			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"username": "test-user"`))
		})

		It("should include caching config when specified", func() {
			repo.Spec.Caching = backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:       "/cache",
				ContentCacheSize:     resource.MustParse("1Gi"),
				MetadataCacheSize:    resource.MustParse("512Mi"),
				MaxListCacheDuration: 60,
			}

			cm, err := buildConfigMap(backup, repo)
			Expect(err).NotTo(HaveOccurred())

			var config map[string]interface{}
			err = json.Unmarshal([]byte(cm.Data["repository.config"]), &config)
			Expect(err).NotTo(HaveOccurred())

			caching, ok := config["caching"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			Expect(caching["cacheDirectory"]).To(Equal("/cache"))
		})
	})

	Context("buildServerModeConfig", func() {
		It("should include server URL and user credentials", func() {
			repo.Spec.Server.Enabled = true
			repo.Status.ServerURL = "https://kopia-server-test-repo.default.svc:51515"
			backup.Status.Username = "user-test-backup"

			envVars, envFrom, volumeMounts, volumes := buildServerModeConfig(
				backup, repo,
				nil, nil, nil, nil,
			)

			Expect(envFrom).NotTo(BeEmpty())

			_ = envVars
			_ = volumes
			_ = volumeMounts
		})

		It("should not include TLS fingerprint env when empty", func() {
			repo.Spec.Server.Enabled = true
			repo.Status.TLSCertFingerprint = ""

			envVars, _, _, _ := buildServerModeConfig(
				backup, repo,
				nil, nil, nil, nil,
			)

			for _, env := range envVars {
				Expect(env.Name).NotTo(Equal("KOPIA_TLS_FINGERPRINT"))
			}
		})

		It("should include TLS fingerprint env when set", func() {
			repo.Spec.Server.Enabled = true
			repo.Status.TLSCertFingerprint = "ABC123"

			envVars, _, _, _ := buildServerModeConfig(
				backup, repo,
				nil, nil, nil, nil,
			)

			found := false
			for _, env := range envVars {
				if env.Name == "KOPIA_TLS_FINGERPRINT" {
					Expect(env.Value).To(Equal("ABC123"))
					found = true
				}
			}
			Expect(found).To(BeTrue(), "Expected KOPIA_TLS_FINGERPRINT env var")
		})
	})

	Context("buildDirectModeConfig", func() {
		It("should include cache directory env and config volume", func() {
			envVars, _, volumeMounts, volumes := buildDirectModeConfig(
				repo, "/tmp/kopia-cache",
				nil, nil, nil, nil,
			)

			envNames := make([]string, 0)
			for _, e := range envVars {
				envNames = append(envNames, e.Name)
			}
			Expect(envNames).To(ContainElement("KOPIA_CACHE_DIRECTORY"))

			volNames := make([]string, 0)
			for _, v := range volumes {
				volNames = append(volNames, v.Name)
			}
			Expect(volNames).To(ContainElement("config"))

			mountPaths := make([]string, 0)
			for _, vm := range volumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElement("/config/repository.config"))
		})

		It("should configure SFTP volumes and mounts for SFTP storage", func() {
			repo.Spec.StorageType = backupv1alpha1.StorageTypeSFTP
			repo.Spec.SFTPOptions = backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
				Host:              "sftp.example.com",
				Port:              22,
				Path:              "/backups",
				CredentialsSecret: "sftp-creds",
			}

			_, _, volumeMounts, volumes := buildDirectModeConfig(
				repo, "/cache/kopia",
				nil, nil, nil, nil,
			)

			volNames := make([]string, 0)
			for _, v := range volumes {
				volNames = append(volNames, v.Name)
			}
			Expect(volNames).To(ContainElement("sftp-credentials"))
			Expect(volNames).To(ContainElement("kopia-cache"))

			mountPaths := make([]string, 0)
			for _, vm := range volumeMounts {
				mountPaths = append(mountPaths, vm.MountPath)
			}
			Expect(mountPaths).To(ContainElement("/sftp-creds"))
			Expect(mountPaths).To(ContainElement("/cache/kopia"))
		})
	})

	Context("naming.CronJobName", func() {
		It("should prefix with snapshot- for short names", func() {
			Expect(naming.CronJobName("my-pvc")).To(Equal("snapshot-my-pvc"))
		})

		It("should truncate long names", func() {
			longName := "this-is-a-very-long-pvc-name-that-exceeds-forty-two-chars-limit"
			result := naming.CronJobName(longName)
			Expect(result).To(HavePrefix("snapshot-"))
			Expect(len(result)).To(BeNumerically("<=", 54))
		})

		It("should handle exactly 42 characters without truncation", func() {
			// 42 chars: "snapshot-" (9) + 42 = 51, well under 54
			name42 := "abcdefghijklmnopqrstuvwxyz0123456789abcdef"
			Expect(name42).To(HaveLen(42))
			result := naming.CronJobName(name42)
			Expect(result).To(Equal("snapshot-" + name42))
		})

		It("should truncate at 43+ characters with last char appended", func() {
			name43 := "abcdefghijklmnopqrstuvwxyz0123456789abcdefg"
			Expect(name43).To(HaveLen(43))
			result := naming.CronJobName(name43)
			Expect(result).To(HavePrefix("snapshot-"))
			Expect(len(result)).To(BeNumerically("<=", 54))
			// Should end with the last character of the original name
			Expect(result).To(HaveSuffix("g"))
		})

		It("should handle empty name", func() {
			result := naming.CronJobName("")
			Expect(result).To(Equal("snapshot-unknown"))
		})
	})

	Context("buildConfigMap edge cases", func() {
		It("should error on unsupported storage type", func() {
			repo.Spec.StorageType = "unknown"
			_, err := buildConfigMap(backup, repo)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unsupported storage type"))
		})
	})
})
