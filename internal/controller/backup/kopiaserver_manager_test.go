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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func TestNewKopiaServerManager(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))

	manager := NewKopiaServerManager(client, scheme, log)

	require.NotNil(t, manager)
	assert.Equal(t, client, manager.Client)
	assert.Equal(t, scheme, manager.Scheme)
}

func TestKopiaServerManager_GetServerURL(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	tests := []struct {
		name        string
		repo        *backupv1alpha1.KopiaRepository
		service     *corev1.Service
		expectedURL string
	}{
		{
			name: "default port",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Exposure: backupv1alpha1.KopiaServerExposureSpec{
							ServicePort: 0,
						},
					},
				},
			},
			service:     &corev1.Service{},
			expectedURL: "https://kopia-server-test-repo.default.svc.cluster.local:51515",
		},
		{
			name: "custom port",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-repo",
					Namespace: "backup-ns",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Exposure: backupv1alpha1.KopiaServerExposureSpec{
							ServicePort: 8443,
						},
					},
				},
			},
			service:     &corev1.Service{},
			expectedURL: "https://kopia-server-my-repo.backup-ns.svc.cluster.local:8443",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			result := manager.GetServerURL(ctx, tt.repo, tt.service)
			assert.Equal(t, tt.expectedURL, result)
		})
	}
}

func TestKopiaServerManager_IsServerReady(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	tests := []struct {
		name          string
		deployment    *appsv1.Deployment
		repo          *backupv1alpha1.KopiaRepository
		expectedReady bool
		expectedError bool
	}{
		{
			name: "deployment ready",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kopia-server-test-repo",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			},
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
			},
			expectedReady: true,
			expectedError: false,
		},
		{
			name: "deployment not ready",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kopia-server-test-repo",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
				},
			},
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
			},
			expectedReady: false,
			expectedError: false,
		},
		{
			name:       "deployment not found",
			deployment: nil,
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
			},
			expectedReady: false,
			expectedError: true,
		},
		{
			name: "deployment with zero replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kopia-server-test-repo",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      0,
					ReadyReplicas: 0,
				},
			},
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
			},
			expectedReady: false,
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clientBuilder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.deployment != nil {
				clientBuilder = clientBuilder.WithObjects(tt.deployment)
			}
			client := clientBuilder.Build()

			manager := NewKopiaServerManager(client, scheme, log)
			ctx := context.Background()

			ready, err := manager.IsServerReady(ctx, tt.repo)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedReady, ready)
		})
	}
}

func TestKopiaServerManager_ConstructServerDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "filesystem",
			RepositoryPassword: "test-password",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled:  true,
				Image:    "ghcr.io/fastlorenzo/kopia:test",
				Replicas: 2,
			},
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
		},
	}

	deploymentName := "kopia-server-test-repo"
	deployment := manager.constructServerDeployment(repo, deploymentName)

	require.NotNil(t, deployment)

	// Verify basic properties
	assert.Equal(t, deploymentName, deployment.Name)
	assert.Equal(t, "default", deployment.Namespace)

	// Verify replicas
	require.NotNil(t, deployment.Spec.Replicas)
	assert.Equal(t, int32(2), *deployment.Spec.Replicas)

	// Verify labels
	assert.Equal(t, "kopia-server", deployment.Labels["app"])
	assert.Equal(t, "test-repo", deployment.Labels["kopia-repository"])
	assert.Equal(t, "kopia-operator", deployment.Labels["app.kubernetes.io/managed-by"])

	// Verify container
	require.Len(t, deployment.Spec.Template.Spec.Containers, 1)
	container := deployment.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "kopia-server", container.Name)
	assert.Equal(t, "ghcr.io/fastlorenzo/kopia:test", container.Image)

	// Verify probes
	require.NotNil(t, container.LivenessProbe)
	require.NotNil(t, container.ReadinessProbe)

	// Verify ports
	require.Len(t, container.Ports, 1)
	assert.Equal(t, int32(51515), container.Ports[0].ContainerPort)

	// Verify environment variables
	var hasKopiaPassword, hasServerUsername, hasServerPassword bool
	for _, env := range container.Env {
		switch env.Name {
		case "KOPIA_PASSWORD":
			hasKopiaPassword = true
			require.NotNil(t, env.ValueFrom)
			require.NotNil(t, env.ValueFrom.SecretKeyRef)
		case "KOPIA_SERVER_USERNAME":
			hasServerUsername = true
		case "KOPIA_SERVER_PASSWORD":
			hasServerPassword = true
			require.NotNil(t, env.ValueFrom)
			require.NotNil(t, env.ValueFrom.SecretKeyRef)
		}
	}
	assert.True(t, hasKopiaPassword, "Expected KOPIA_PASSWORD env var")
	assert.True(t, hasServerUsername, "Expected KOPIA_SERVER_USERNAME env var")
	assert.True(t, hasServerPassword, "Expected KOPIA_SERVER_PASSWORD env var")
}

func TestKopiaServerManager_ConstructServerService(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	tests := []struct {
		name                string
		repo                *backupv1alpha1.KopiaRepository
		expectedServiceType corev1.ServiceType
		expectedPort        int32
	}{
		{
			name: "default service type and port",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Exposure: backupv1alpha1.KopiaServerExposureSpec{},
					},
				},
			},
			expectedServiceType: corev1.ServiceTypeClusterIP,
			expectedPort:        51515,
		},
		{
			name: "custom service type and port",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-repo",
					Namespace: "default",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						Exposure: backupv1alpha1.KopiaServerExposureSpec{
							ServiceType: corev1.ServiceTypeLoadBalancer,
							ServicePort: 8443,
						},
					},
				},
			},
			expectedServiceType: corev1.ServiceTypeLoadBalancer,
			expectedPort:        8443,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceName := "kopia-server-test-repo"
			service := manager.constructServerService(tt.repo, serviceName)

			require.NotNil(t, service)
			assert.Equal(t, serviceName, service.Name)
			assert.Equal(t, tt.expectedServiceType, service.Spec.Type)
			require.Len(t, service.Spec.Ports, 1)
			assert.Equal(t, tt.expectedPort, service.Spec.Ports[0].Port)
			assert.Equal(t, "api", service.Spec.Ports[0].Name)
		})
	}
}

func TestKopiaServerManager_ConstructStorageVolume(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	tests := []struct {
		name           string
		repo           *backupv1alpha1.KopiaRepository
		expectNFS      bool
		expectHostPath bool
	}{
		{
			name: "NFS volume",
			repo: &backupv1alpha1.KopiaRepository{
				Spec: backupv1alpha1.KopiaRepositorySpec{
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path:      "/backup",
						NFSServer: "nfs.example.com",
						NFSPath:   "/exports/backup",
					},
				},
			},
			expectNFS:      true,
			expectHostPath: false,
		},
		{
			name: "HostPath volume",
			repo: &backupv1alpha1.KopiaRepository{
				Spec: backupv1alpha1.KopiaRepositorySpec{
					FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
						Path: "/backup",
					},
				},
			},
			expectNFS:      false,
			expectHostPath: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volume := manager.constructStorageVolume(tt.repo)

			assert.Equal(t, "repository", volume.Name)

			if tt.expectNFS {
				require.NotNil(t, volume.NFS)
				assert.Equal(t, "nfs.example.com", volume.NFS.Server)
				assert.Equal(t, "/exports/backup", volume.NFS.Path)
			}

			if tt.expectHostPath {
				require.NotNil(t, volume.HostPath)
				assert.Equal(t, "/backup", volume.HostPath.Path)
			}
		})
	}
}

func TestKopiaServerManager_EnsureServerDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "filesystem",
			RepositoryPassword: "test-password",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
	}

	t.Run("create new deployment", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		deployment, err := manager.EnsureServerDeployment(ctx, repo)

		require.NoError(t, err)
		require.NotNil(t, deployment)
		assert.Equal(t, "kopia-server-test-repo", deployment.Name)

		// Verify deployment was created in cluster
		createdDeployment := &appsv1.Deployment{}
		err = client.Get(ctx, types.NamespacedName{
			Name:      "kopia-server-test-repo",
			Namespace: "default",
		}, createdDeployment)
		require.NoError(t, err)
	})

	t.Run("update existing deployment", func(t *testing.T) {
		existingDeployment := &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kopia-server-test-repo",
				Namespace: "default",
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "kopia-server"},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": "kopia-server"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "old-container"},
						},
					},
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingDeployment).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		deployment, err := manager.EnsureServerDeployment(ctx, repo)

		require.NoError(t, err)
		require.NotNil(t, deployment)

		// Verify container was updated
		assert.Equal(t, "kopia-server", deployment.Spec.Template.Spec.Containers[0].Name)
	})
}

func TestKopiaServerManager_EnsureServerService(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
	}

	t.Run("create new service", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		service, err := manager.EnsureServerService(ctx, repo)

		require.NoError(t, err)
		require.NotNil(t, service)
		assert.Equal(t, "kopia-server-test-repo", service.Name)

		// Verify service was created in cluster
		createdService := &corev1.Service{}
		err = client.Get(ctx, types.NamespacedName{
			Name:      "kopia-server-test-repo",
			Namespace: "default",
		}, createdService)
		require.NoError(t, err)
	})
}

func TestKopiaServerManager_EnsureRepositoryPasswordSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	t.Run("skip when using existing secret", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-repo",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				RepositoryPasswordExistingSecret: "existing-secret",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		err := manager.EnsureRepositoryPasswordSecret(ctx, repo)

		require.NoError(t, err)

		// Verify no secret was created
		secret := &corev1.Secret{}
		err = client.Get(ctx, types.NamespacedName{
			Name:      "kopia-repo-test-repo",
			Namespace: "default",
		}, secret)
		require.Error(t, err) // Should not find the secret
	})

	t.Run("create secret with password", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-repo",
				Namespace: "default",
				UID:       "test-uid",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				RepositoryPassword: "my-secret-password",
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		err := manager.EnsureRepositoryPasswordSecret(ctx, repo)

		require.NoError(t, err)

		// Verify secret was created
		secret := &corev1.Secret{}
		err = client.Get(ctx, types.NamespacedName{
			Name:      "kopia-repo-test-repo",
			Namespace: "default",
		}, secret)
		require.NoError(t, err)
	})

	t.Run("error when no password configured", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-repo",
				Namespace: "default",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := NewKopiaServerManager(client, scheme, log)

		ctx := context.Background()
		err := manager.EnsureRepositoryPasswordSecret(ctx, repo)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "repository password must be set")
	})
}

func TestBuildCacheFlags(t *testing.T) {
	tests := []struct {
		name     string
		caching  backupv1alpha1.KopiaRepositoryCachingSpec
		expected []string
		notIn    []string
	}{
		{
			name: "all flags set",
			caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:              "/cache",
				ContentCacheSizeBytes:       1048576 * 100, // 100MB
				ContentCacheSizeLimitBytes:  1048576 * 200, // 200MB
				MetadataCacheSizeBytes:      1048576 * 50,  // 50MB
				MetadataCacheSizeLimitBytes: 1048576 * 100, // 100MB
				MaxListCacheDuration:        60,
				MinMetadataSweepAge:         30,
				MinContentSweepAge:          30,
				MinIndexSweepAge:            30,
			},
			expected: []string{
				"--cache-directory=/cache",
				"--content-cache-size-mb=100",
				"--content-cache-size-limit-mb=200",
				"--metadata-cache-size-mb=50",
				"--metadata-cache-size-limit-mb=100",
				"--max-list-cache-duration=60s",
				"--min-metadata-sweep-age=30s",
				"--min-content-sweep-age=30s",
				"--min-index-sweep-age=30s",
			},
		},
		{
			name:    "empty caching spec",
			caching: backupv1alpha1.KopiaRepositoryCachingSpec{},
			notIn: []string{
				"--cache-directory",
				"--content-cache-size-mb",
				"--metadata-cache-size-mb",
			},
		},
		{
			name: "partial flags",
			caching: backupv1alpha1.KopiaRepositoryCachingSpec{
				CacheDirectory:        "/cache",
				ContentCacheSizeBytes: 1048576 * 100,
				MaxListCacheDuration:  60,
			},
			expected: []string{
				"--cache-directory=/cache",
				"--content-cache-size-mb=100",
				"--max-list-cache-duration=60s",
			},
			notIn: []string{
				"--metadata-cache-size-mb",
				"--min-metadata-sweep-age",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildCacheFlags(tt.caching)

			for _, exp := range tt.expected {
				assert.Contains(t, result, exp)
			}

			for _, notExp := range tt.notIn {
				assert.NotContains(t, result, notExp)
			}
		})
	}
}

func TestKopiaServerManager_GetTLSSecretName(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	tests := []struct {
		name         string
		repo         *backupv1alpha1.KopiaRepository
		expectedName string
	}{
		{
			name: "user-provided secret name",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-repo",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						TLS: backupv1alpha1.KopiaServerTLSSpec{
							SecretName: "my-custom-tls-secret",
						},
					},
				},
			},
			expectedName: "my-custom-tls-secret",
		},
		{
			name: "auto-generated secret name",
			repo: &backupv1alpha1.KopiaRepository{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-repo",
				},
				Spec: backupv1alpha1.KopiaRepositorySpec{
					Server: backupv1alpha1.KopiaServerSpec{
						TLS: backupv1alpha1.KopiaServerTLSSpec{},
					},
				},
			},
			expectedName: "kopia-server-tls-test-repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := manager.getTLSSecretName(tt.repo)
			assert.Equal(t, tt.expectedName, result)
		})
	}
}

func TestKopiaServerManager_ConstructServerCommand(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))
	manager := NewKopiaServerManager(client, scheme, log)

	t.Run("filesystem storage", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-repo",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				Hostname:    "test-host",
				Username:    "test-user",
				StorageType: "filesystem",
			},
		}

		cmd := manager.constructServerCommand(repo)

		assert.Contains(t, cmd, "kopia repository connect filesystem")
		assert.Contains(t, cmd, "--path=/repository")
		assert.Contains(t, cmd, "--override-hostname=test-host")
		assert.Contains(t, cmd, "--override-username=test-user")
		assert.Contains(t, cmd, "kopia server start")
		assert.Contains(t, cmd, "--tls-cert-file=/tls/tls.crt")
		assert.Contains(t, cmd, "--tls-key-file=/tls/tls.key")
	})

	t.Run("SFTP storage", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-repo",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				Hostname:    "test-host",
				Username:    "test-user",
				StorageType: "sftp",
				SFTPOptions: backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
					Host: "sftp.example.com",
					Port: 22,
					Path: "/backup",
				},
			},
		}

		cmd := manager.constructServerCommand(repo)

		assert.Contains(t, cmd, "kopia repository connect sftp")
		assert.Contains(t, cmd, "--host=sftp.example.com")
		assert.Contains(t, cmd, "--port=22")
		assert.Contains(t, cmd, "--path=/backup")
		assert.Contains(t, cmd, "kopia server start")
	})

	t.Run("with extra args", func(t *testing.T) {
		repo := &backupv1alpha1.KopiaRepository{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-repo",
			},
			Spec: backupv1alpha1.KopiaRepositorySpec{
				Hostname:    "test-host",
				Username:    "test-user",
				StorageType: "filesystem",
				Server: backupv1alpha1.KopiaServerSpec{
					ExtraArgs: []string{"--insecure", "--no-ui"},
				},
			},
		}

		cmd := manager.constructServerCommand(repo)

		assert.Contains(t, cmd, "--insecure")
		assert.Contains(t, cmd, "--no-ui")
	})
}
