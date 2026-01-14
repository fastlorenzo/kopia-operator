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
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func setupRepoTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return scheme
}

func TestKopiaRepositoryReconciler_Reconcile_ResourceNotFound(t *testing.T) {
	scheme := setupRepoTestScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaRepositoryReconciler_Reconcile_UnsupportedStorageType(t *testing.T) {
	scheme := setupRepoTestScheme()

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "unsupported-type",
			RepositoryPassword: "test-password",
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err) // Unsupported storage is handled gracefully
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaRepositoryReconciler_Reconcile_MissingPassword(t *testing.T) {
	scheme := setupRepoTestScheme()

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:    "test-host",
			Username:    "test-user",
			StorageType: "filesystem",
			// No password configured
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err) // Missing password is handled gracefully
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaRepositoryReconciler_Reconcile_DirectAccess(t *testing.T) {
	scheme := setupRepoTestScheme()

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
				Enabled: false,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)

	// Verify status was updated for direct access mode
	updatedRepo := &backupv1alpha1.KopiaRepository{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "test-repo",
		Namespace: "default",
	}, updatedRepo)
	require.NoError(t, err)
	assert.False(t, updatedRepo.Status.ServerReady)
	assert.Empty(t, updatedRepo.Status.ServerURL)
}

func TestKopiaRepositoryReconciler_Reconcile_ServerMode(t *testing.T) {
	scheme := setupRepoTestScheme()

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
			FileSystemOptions: backupv1alpha1.KopiaRepositoryStorageFileSystemSpec{
				Path:      "/backup",
				NFSServer: "nfs.example.com",
				NFSPath:   "/exports/backup",
			},
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	// Should requeue to wait for server to be ready
	assert.Equal(t, int64(10*1000000000), int64(result.RequeueAfter))

	// Verify deployment was created
	deployment := &appsv1.Deployment{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-server-test-repo",
		Namespace: "default",
	}, deployment)
	require.NoError(t, err)

	// Verify service was created
	service := &corev1.Service{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-server-test-repo",
		Namespace: "default",
	}, service)
	require.NoError(t, err)

	// Verify TLS secret was created
	tlsSecret := &corev1.Secret{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-server-tls-test-repo",
		Namespace: "default",
	}, tlsSecret)
	require.NoError(t, err)
	assert.Contains(t, tlsSecret.Data, "tls.crt")
	assert.Contains(t, tlsSecret.Data, "tls.key")
	assert.Contains(t, tlsSecret.Data, "fingerprint")
}

func TestKopiaRepositoryReconciler_Reconcile_SFTPStorage(t *testing.T) {
	scheme := setupRepoTestScheme()

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:           "test-host",
			Username:           "test-user",
			StorageType:        "sftp",
			RepositoryPassword: "test-password",
			SFTPOptions: backupv1alpha1.KopiaRepositoryStorageSFTPSpec{
				Host:              "sftp.example.com",
				Port:              22,
				Path:              "/backup",
				CredentialsSecret: "sftp-creds",
			},
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: false,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaRepositoryReconciler_Reconcile_WithExistingSecret(t *testing.T) {
	scheme := setupRepoTestScheme()

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-password-secret",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"KOPIA_PASSWORD": []byte("my-secret-password"),
		},
	}

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
		Spec: backupv1alpha1.KopiaRepositorySpec{
			Hostname:                         "test-host",
			Username:                         "test-user",
			StorageType:                      "filesystem",
			RepositoryPasswordExistingSecret: "existing-password-secret",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: false,
			},
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(existingSecret, repo).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	assert.Equal(t, reconcile.Result{}, result)
}

func TestKopiaRepositoryReconciler_UpdateCondition(t *testing.T) {
	scheme := setupRepoTestScheme()

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

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Scheme: scheme,
		Log:    log,
	}

	// Test adding new condition
	reconciler.updateCondition(repo, "Ready", metav1.ConditionTrue, "TestReason", "Test message")

	require.Len(t, repo.Status.Conditions, 1)
	assert.Equal(t, "Ready", repo.Status.Conditions[0].Type)
	assert.Equal(t, metav1.ConditionTrue, repo.Status.Conditions[0].Status)
	assert.Equal(t, "TestReason", repo.Status.Conditions[0].Reason)
	assert.Equal(t, "Test message", repo.Status.Conditions[0].Message)

	// Test updating existing condition
	reconciler.updateCondition(repo, "Ready", metav1.ConditionFalse, "UpdatedReason", "Updated message")

	require.Len(t, repo.Status.Conditions, 1)
	assert.Equal(t, metav1.ConditionFalse, repo.Status.Conditions[0].Status)
	assert.Equal(t, "UpdatedReason", repo.Status.Conditions[0].Reason)
	assert.Equal(t, "Updated message", repo.Status.Conditions[0].Message)

	// Test adding second condition
	reconciler.updateCondition(repo, "ServerReady", metav1.ConditionTrue, "ServerRunning", "Server is running")

	require.Len(t, repo.Status.Conditions, 2)
}

func TestKopiaRepositoryReconciler_ServerModeServerReady(t *testing.T) {
	scheme := setupRepoTestScheme()

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

	// Create a ready deployment
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kopia-server-test-repo",
			Namespace: "default",
		},
		Status: appsv1.DeploymentStatus{
			Replicas:      1,
			ReadyReplicas: 1,
		},
	}

	// Create a TLS secret (simulating it was already created)
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kopia-server-tls-test-repo",
			Namespace: "default",
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt":     []byte("cert-data"),
			"tls.key":     []byte("key-data"),
			"fingerprint": []byte("ABC123"),
		},
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&backupv1alpha1.KopiaRepository{}).
		WithObjects(repo, deployment, tlsSecret).
		Build()

	log := zap.New(zap.UseDevMode(true))

	reconciler := &KopiaRepositoryReconciler{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-repo",
			Namespace: "default",
		},
	})

	require.NoError(t, err)
	// Server is ready, should not requeue
	assert.Equal(t, reconcile.Result{}, result)

	// Verify status was updated
	updatedRepo := &backupv1alpha1.KopiaRepository{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "test-repo",
		Namespace: "default",
	}, updatedRepo)
	require.NoError(t, err)
	assert.True(t, updatedRepo.Status.ServerReady)
	assert.Contains(t, updatedRepo.Status.ServerURL, "kopia-server-test-repo")
	assert.Equal(t, "kopia-server-test-repo", updatedRepo.Status.ServerDeployment)
	assert.Equal(t, "kopia-server-test-repo", updatedRepo.Status.ServerService)
}
