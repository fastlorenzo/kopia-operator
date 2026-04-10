package server

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

var _ = Describe("Server Manager Helpers", func() {
	Context("BuildCacheFlags", func() {
		It("should return empty for zero-value caching spec", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{}
			Expect(BuildCacheFlags(spec)).To(BeEmpty())
		})

		It("should include cache size flags when set", func() {
			spec := backupv1alpha1.KopiaRepositoryCachingSpec{
				ContentCacheSize:     resource.MustParse("500Mi"),
				MetadataCacheSize:    resource.MustParse("100Mi"),
				MaxListCacheDuration: 60,
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

var _ = Describe("ServerManager Integration", func() {
	var (
		ctx  context.Context
		mgr  *KopiaServerManager
		ns   string
		nsID int
	)

	BeforeEach(func() {
		ctx = context.Background()
		nsID++
		ns = fmt.Sprintf("server-test-%d", nsID)
		nsObj := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, nsObj)

		mgr = NewKopiaServerManager(k8sClient, scheme.Scheme)
	})

	makeRepo := func(name string) *backupv1alpha1.KopiaRepository {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				StorageType:        backupv1alpha1.StorageTypeFilesystem,
				PasswordSecretName: "kopia-pass",
				Hostname:           "kopia-host",
				Username:           "admin",
				FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
					Path: "/data/repo",
				},
				Server: backupv1alpha1.KopiaServerSpec{
					Enabled:  true,
					Image:    "kopia/kopia:latest",
					Replicas: 1,
					Exposure: backupv1alpha1.KopiaServerExposureSpec{
						ServiceType: corev1.ServiceTypeClusterIP,
						ServicePort: 51515,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, repo)).To(Succeed())
		return repo
	}

	Context("EnsureServerDeployment", func() {
		It("creates a new deployment", func() {
			repo := makeRepo("deploy-create")

			Expect(mgr.EnsureServerDeployment(ctx, repo)).To(Succeed())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: naming.ServerDeploymentName("deploy-create"), Namespace: ns,
			}, deploy)).To(Succeed())
			Expect(*deploy.Spec.Replicas).To(Equal(int32(1)))
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("kopia/kopia:latest"))
		})

		It("updates an existing deployment", func() {
			repo := makeRepo("deploy-update")
			Expect(mgr.EnsureServerDeployment(ctx, repo)).To(Succeed())

			// Change image and re-ensure
			repo.Spec.Server.Image = "kopia/kopia:v2"
			Expect(mgr.EnsureServerDeployment(ctx, repo)).To(Succeed())

			deploy := &appsv1.Deployment{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: naming.ServerDeploymentName("deploy-update"), Namespace: ns,
			}, deploy)).To(Succeed())
			Expect(deploy.Spec.Template.Spec.Containers[0].Image).To(Equal("kopia/kopia:v2"))
		})
	})

	Context("EnsureServerService", func() {
		It("creates a new service", func() {
			repo := makeRepo("svc-create")

			Expect(mgr.EnsureServerService(ctx, repo)).To(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: naming.ServerServiceName("svc-create"), Namespace: ns,
			}, svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(51515)))
		})

		It("updates an existing service port", func() {
			repo := makeRepo("svc-update")
			Expect(mgr.EnsureServerService(ctx, repo)).To(Succeed())

			repo.Spec.Server.Exposure.ServicePort = 12345
			Expect(mgr.EnsureServerService(ctx, repo)).To(Succeed())

			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: naming.ServerServiceName("svc-update"), Namespace: ns,
			}, svc)).To(Succeed())
			Expect(svc.Spec.Ports[0].Port).To(Equal(int32(12345)))
		})
	})

	Context("EnsureTLSSecret", func() {
		It("auto-generates a TLS secret with valid fingerprint", func() {
			repo := makeRepo("tls-auto")

			fingerprint, err := mgr.EnsureTLSSecret(ctx, repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(fingerprint).To(MatchRegexp(`^[0-9A-F]{64}$`))

			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: naming.TLSSecretName("tls-auto"), Namespace: ns,
			}, secret)).To(Succeed())
			Expect(secret.Data).To(HaveKey("tls.crt"))
			Expect(secret.Data).To(HaveKey("tls.key"))
			Expect(secret.Data).To(HaveKey("fingerprint"))
		})

		It("returns existing fingerprint on second call", func() {
			repo := makeRepo("tls-idempotent")

			fp1, err := mgr.EnsureTLSSecret(ctx, repo)
			Expect(err).NotTo(HaveOccurred())

			fp2, err := mgr.EnsureTLSSecret(ctx, repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(fp2).To(Equal(fp1))
		})

		It("errors when user-provided secret doesn't exist", func() {
			repo := makeRepo("tls-user-missing")
			repo.Spec.Server.TLS.SecretName = "nonexistent-tls"

			_, err := mgr.EnsureTLSSecret(ctx, repo)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("IsServerReady", func() {
		It("returns false when deployment has no ready replicas", func() {
			repo := makeRepo("ready-check")
			Expect(mgr.EnsureServerDeployment(ctx, repo)).To(Succeed())

			ready, err := mgr.IsServerReady(ctx, repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(ready).To(BeFalse())
		})

		It("errors when deployment doesn't exist", func() {
			repo := makeRepo("ready-missing")
			_, err := mgr.IsServerReady(ctx, repo)
			Expect(err).To(HaveOccurred())
		})
	})
})
