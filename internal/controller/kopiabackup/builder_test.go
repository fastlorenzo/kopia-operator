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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
			Expect(cmd).To(ContainSubstring("kopia snap create /data"))
		})

		It("should build a server mode command when server is enabled", func() {
			repo.Spec.Server.Enabled = true
			backup.Status.Username = "user-test-backup"

			cmd := buildBackupCommand(backup, repo, "/data")
			Expect(cmd).To(ContainSubstring("kopia repository connect server"))
			Expect(cmd).To(ContainSubstring("kopia snap create /data"))
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
		})

		It("should include hostname and username", func() {
			cm, err := buildConfigMap(backup, repo)
			Expect(err).NotTo(HaveOccurred())

			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"hostname": "test-host"`))
			Expect(cm.Data["repository.config"]).To(ContainSubstring(`"username": "test-user"`))
		})

		It("should include caching config when specified", func() {
			repo.Spec.Caching = backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:         "/cache",
				ContentCacheSizeBytes:  1024 * 1024 * 1024,
				MetadataCacheSizeBytes: 512 * 1024 * 1024,
				MaxListCacheDuration:   60,
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
	})

	Context("int32Ptr", func() {
		It("should return a pointer to the value", func() {
			p := int32Ptr(42)
			Expect(*p).To(Equal(int32(42)))
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
	})
})
