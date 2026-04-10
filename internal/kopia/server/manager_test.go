package server

import (
	"crypto/x509"
	"encoding/pem"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

var _ = Describe("Server Manager Helpers", func() {
	Context("BuildCacheFlags", func() {
		It("should return empty for zero-value caching spec", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{}
			Expect(BuildCacheFlags(spec)).To(BeEmpty())
		})

		It("should include cache size flags when set", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{
				ContentCacheSizeBytes:  500 * 1024 * 1024,
				MetadataCacheSizeBytes: 100 * 1024 * 1024,
				MaxListCacheDuration:   60,
			}
			flags := BuildCacheFlags(spec)
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
			certPEM, keyPEM, err := generateSelfSignedCert("test.local", []string{"test.local"})
			Expect(err).NotTo(HaveOccurred())
			Expect(certPEM).NotTo(BeEmpty())
			Expect(keyPEM).NotTo(BeEmpty())
			Expect(string(certPEM)).To(ContainSubstring("BEGIN CERTIFICATE"))
			Expect(string(keyPEM)).To(ContainSubstring("BEGIN"))
		})

		It("should generate a certificate valid for 1 year", func() {
			certPEM, _, err := generateSelfSignedCert("test.local", []string{"test.local"})
			Expect(err).NotTo(HaveOccurred())

			block, _ := pem.Decode(certPEM)
			Expect(block).NotTo(BeNil())
			cert, err := x509.ParseCertificate(block.Bytes)
			Expect(err).NotTo(HaveOccurred())

			validity := cert.NotAfter.Sub(cert.NotBefore)
			Expect(validity).To(BeNumerically("~", 365*24*time.Hour, time.Hour))
		})
	})

	Context("certNeedsRotation", func() {
		It("should return false for a freshly generated cert", func() {
			certPEM, _, err := generateSelfSignedCert("test.local", []string{"test.local"})
			Expect(err).NotTo(HaveOccurred())

			needsRotation, _ := certNeedsRotation(certPEM, 30*24*time.Hour)
			Expect(needsRotation).To(BeFalse())
		})

		It("should return true for invalid PEM data", func() {
			needsRotation, reason := certNeedsRotation([]byte("not-a-cert"), 30*24*time.Hour)
			Expect(needsRotation).To(BeTrue())
			Expect(reason).To(ContainSubstring("invalid PEM"))
		})
	})

	Context("calculateCertFingerprint", func() {
		It("should calculate SHA256 fingerprint from cert PEM", func() {
			certPEM, _, err := generateSelfSignedCert("test.local", []string{"test.local"})
			Expect(err).NotTo(HaveOccurred())

			fp, err := calculateCertFingerprint(certPEM)
			Expect(err).NotTo(HaveOccurred())
			Expect(fp).To(MatchRegexp(`^[0-9A-F]{64}$`))
		})
	})

	Context("constructServerDeployment", func() {
		It("should create a deployment with correct labels and spec", func() {
			mgr := &KopiaServerManager{}
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
			mgr := &KopiaServerManager{}
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
