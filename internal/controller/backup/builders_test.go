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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
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

			// Check volumes exist
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
			Expect(cm.Name).To(Equal("kopia-config-test-repo"))
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

			// Check envFrom has the user credentials secret reference
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

			// Should have config volume for repository.config
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
})

var _ = Describe("Server Manager Helpers", func() {
	Context("buildCacheFlags", func() {
		It("should return empty for zero-value caching spec", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{}
			Expect(buildCacheFlags(spec)).To(BeEmpty())
		})

		It("should include cache size flags when set", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{
				ContentCacheSizeBytes:  500 * 1024 * 1024,
				MetadataCacheSizeBytes: 100 * 1024 * 1024,
				MaxListCacheDuration:   60,
			}
			flags := buildCacheFlags(spec)
			Expect(flags).To(ContainSubstring("--content-cache-size-mb=500"))
			Expect(flags).To(ContainSubstring("--metadata-cache-size-mb=100"))
			Expect(flags).To(ContainSubstring("--max-list-cache-duration=60s"))
		})
	})

	Context("constructStorageVolume", func() {
		It("should create an NFS volume when NFSServer is set", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "test-repo"},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType: backupv1alpha1.StorageTypeFilesystem,
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						NFSServer: "10.0.0.1",
						NFSPath:   "/export/backup",
					},
				},
			}
			vol := mgr.constructStorageVolume(repo)
			Expect(vol.Name).To(Equal("repository"))
			Expect(vol.VolumeSource.NFS).NotTo(BeNil())
			Expect(vol.VolumeSource.NFS.Server).To(Equal("10.0.0.1"))
			Expect(vol.VolumeSource.NFS.Path).To(Equal("/export/backup"))
		})

		It("should create a HostPath volume for filesystem without NFS", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "test-repo"},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType: backupv1alpha1.StorageTypeFilesystem,
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/data/repo",
					},
				},
			}
			vol := mgr.constructStorageVolume(repo)
			Expect(vol.Name).To(Equal("repository"))
			Expect(vol.VolumeSource.HostPath).NotTo(BeNil())
			Expect(vol.VolumeSource.HostPath.Path).To(Equal("/data/repo"))
		})
	})

	Context("constructServerCommand", func() {
		It("should include filesystem connect command", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "test-repo"},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType: backupv1alpha1.StorageTypeFilesystem,
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/data/repo",
					},
				},
			}
			cmd := mgr.constructServerCommand(repo)
			Expect(cmd).To(ContainSubstring("kopia repository connect filesystem"))
			Expect(cmd).To(ContainSubstring("kopia server start"))
		})

		It("should include SFTP connect command when configured", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "test-repo"},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType: backupv1alpha1.StorageTypeSFTP,
					SFTPOptions: backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
						Host: "sftp.example.com",
						Port: 22,
						Path: "/backups/kopia",
					},
				},
			}
			cmd := mgr.constructServerCommand(repo)
			Expect(cmd).To(ContainSubstring("kopia repository connect sftp"))
			Expect(cmd).To(ContainSubstring("sftp.example.com"))
			Expect(cmd).To(ContainSubstring("kopia server start"))
		})
	})

	Context("GetServerURL", func() {
		It("should construct the correct URL", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-repo",
					Namespace: "prod",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Enabled: true,
						Exposure: backupv1alpha1.KopiaServerExposureSpec{
							ServicePort: 51515,
						},
					},
				},
			}
			url := mgr.GetServerURL(repo)
			Expect(url).To(Equal("https://kopia-server-my-repo.prod.svc.cluster.local:51515"))
		})

		It("should use default port 51515 when not specified", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-repo",
					Namespace: "ns",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{Enabled: true},
				},
			}
			url := mgr.GetServerURL(repo)
			Expect(url).To(ContainSubstring(":51515"))
		})
	})

	Context("getRepositoryPasswordSecretKeyRef", func() {
		It("should use the PasswordSecretName", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				Spec: backupv1alpha1.KopiaRepositorySpec{
					PasswordSecretName: "my-pass-secret",
				},
			}
			ref := mgr.getRepositoryPasswordSecretKeyRef(repo)
			Expect(ref.Name).To(Equal("my-pass-secret"))
		})
	})

	Context("getServerAdminPasswordSecretKeyRef", func() {
		It("should use AdminPasswordSecretName", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						AdminPasswordSecretName: "admin-secret",
					},
				},
			}
			ref := mgr.getServerAdminPasswordSecretKeyRef(repo)
			Expect(ref.Name).To(Equal("admin-secret"))
			Expect(ref.Key).To(Equal("password"))
		})

		It("should fall back to PasswordSecretName when admin secret not set", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				Spec: backupv1alpha1.KopiaRepositorySpec{
					PasswordSecretName: "repo-pass",
					Server: backupv1alpha1.KopiaServerSpec{
						AdminPasswordSecretName: "",
					},
				},
			}
			ref := mgr.getServerAdminPasswordSecretKeyRef(repo)
			Expect(ref.Name).To(Equal("repo-pass"))
		})
	})

	Context("getTLSSecretName", func() {
		It("should construct the TLS secret name from repo name", func() {
			mgr := &KopiaServerManager{}
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{Name: "my-repo"},
			}
			Expect(mgr.getTLSSecretName(repo)).To(Equal("kopia-server-tls-my-repo"))
		})
	})

	Context("generateSelfSignedCert", func() {
		It("should generate valid cert and key PEM data", func() {
			certPEM, keyPEM, err := generateSelfSignedCert("test.local", []string{"test.local"}, 51515)
			Expect(err).NotTo(HaveOccurred())
			Expect(certPEM).NotTo(BeEmpty())
			Expect(keyPEM).NotTo(BeEmpty())
			Expect(string(certPEM)).To(ContainSubstring("BEGIN CERTIFICATE"))
			Expect(string(keyPEM)).To(ContainSubstring("BEGIN"))
		})
	})

	Context("calculateCertFingerprint", func() {
		It("should calculate SHA256 fingerprint from cert PEM", func() {
			certPEM, _, err := generateSelfSignedCert("test.local", []string{"test.local"}, 51515)
			Expect(err).NotTo(HaveOccurred())

			fp, err := calculateCertFingerprint(certPEM)
			Expect(err).NotTo(HaveOccurred())
			Expect(fp).To(MatchRegexp(`^[0-9A-F]{64}$`))
		})
	})

	Context("constructServerDeployment", func() {
		It("should create a deployment with correct labels and spec", func() {
			mgr := NewKopiaServerManager(k8sClient, k8sClient.Scheme())
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					StorageType:        backupv1alpha1.StorageTypeFilesystem,
					PasswordSecretName: "kopia-pass",
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/data/repo",
					},
					Server: backupv1alpha1.KopiaServerSpec{
						Enabled:  true,
						Image:    "kopia/kopia:latest",
						Replicas: 1,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			}
			deploy := mgr.constructServerDeployment(repo, "kopia-server-test-repo")
			Expect(deploy.Name).To(Equal("kopia-server-test-repo"))
			Expect(deploy.Namespace).To(Equal("default"))
			Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("kopia/kopia:latest"))
		})
	})

	Context("constructServerService", func() {
		It("should create a service targeting the server deployment", func() {
			mgr := NewKopiaServerManager(k8sClient, k8sClient.Scheme())
			repo := &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Enabled: true,
						Exposure: backupv1alpha1.KopiaServerExposureSpec{
							ServiceType: "ClusterIP",
							ServicePort: 51515,
						},
					},
				},
			}
			svc := mgr.constructServerService(repo, "kopia-server-test-repo")
			Expect(svc.Name).To(Equal("kopia-server-test-repo"))
			Expect(svc.Namespace).To(Equal("default"))
			Expect(svc.Spec.Ports).To(HaveLen(1))
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(51515)))
		})
	})
})

var _ = Describe("User Manager Helpers", func() {
	Context("generateSecurePassword", func() {
		It("should generate a password of the correct length", func() {
			pw, err := generateSecurePassword(32)
			Expect(err).NotTo(HaveOccurred())
			Expect(pw).To(HaveLen(32))
		})

		It("should generate different passwords each time", func() {
			pw1, _ := generateSecurePassword(32)
			pw2, _ := generateSecurePassword(32)
			Expect(pw1).NotTo(Equal(pw2))
		})
	})

	Context("ServerNotReadyError", func() {
		It("should implement error interface", func() {
			err := &ServerNotReadyError{Message: "deployment not found"}
			Expect(err.Error()).To(ContainSubstring("deployment not found"))
		})
	})
})

var _ = Describe("Helpers", func() {
	Context("getCronJobNameFromPVCName", func() {
		It("should prefix with snapshot-", func() {
			Expect(getCronJobNameFromPVCName("my-pvc")).To(Equal("snapshot-my-pvc"))
		})

		It("should truncate long names to fit CronJob name limits", func() {
			longName := "this-is-a-very-long-pvc-name-that-exceeds-forty-two-characters-limit"
			result := getCronJobNameFromPVCName(longName)
			Expect(result).To(HavePrefix("snapshot-"))
			Expect(len(result)).To(BeNumerically("<=", 54))
		})

		It("should handle empty string", func() {
			Expect(getCronJobNameFromPVCName("")).To(Equal("snapshot-"))
		})
	})
})
