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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

func TestServerNotReadyError(t *testing.T) {
	err := &ServerNotReadyError{Message: "test error message"}
	assert.Equal(t, "test error message", err.Error())
}

func TestKopiaUserManager_GenerateSecurePassword(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	log := zap.New(zap.UseDevMode(true))

	// Create manager without rest config (we're not testing exec functionality)
	manager := &KopiaUserManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	t.Run("generate password of specified length", func(t *testing.T) {
		password, err := manager.generateSecurePassword(32)
		require.NoError(t, err)
		assert.Len(t, password, 32)
	})

	t.Run("passwords are unique", func(t *testing.T) {
		password1, err := manager.generateSecurePassword(32)
		require.NoError(t, err)

		password2, err := manager.generateSecurePassword(32)
		require.NoError(t, err)

		assert.NotEqual(t, password1, password2)
	})

	t.Run("short password", func(t *testing.T) {
		password, err := manager.generateSecurePassword(8)
		require.NoError(t, err)
		assert.Len(t, password, 8)
	})
}

func TestKopiaUserManager_EnsureUser_CreateNew(t *testing.T) {
	// NOTE: This test verifies that EnsureUser creates the secret and returns
	// ServerNotReadyError when no server pod is available. The secret is created
	// before attempting to create the user on the server.

	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
			UID:       "test-uid",
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
			Hostname: "test-host",
			Username: "test-user",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
		Status: backupv1alpha1.KopiaRepositoryStatus{
			ServerReady:        true,
			TLSCertFingerprint: "ABC123",
		},
	}

	// No server pod exists - createUserOnServer will fail with ServerNotReadyError
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	manager := &KopiaUserManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()

	// createUserOnServer will fail because no server pod exists
	// and will return ServerNotReadyError
	_, err := manager.EnsureUser(ctx, backup, repo)

	// Should return error because server pod not found
	require.Error(t, err)
	var serverNotReady *ServerNotReadyError
	assert.True(t, errors.As(err, &serverNotReady))

	// Verify secret was still created before the error
	secret := &corev1.Secret{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-backup-user-default-test-pvc",
		Namespace: "default",
	}, secret)
	require.NoError(t, err)

	// Note: fake client stores StringData, not Data. Check StringData.
	assert.Contains(t, secret.StringData, "KOPIA_SERVER_USERNAME")
	assert.Contains(t, secret.StringData, "KOPIA_SERVER_PASSWORD")

	username := secret.StringData["KOPIA_SERVER_USERNAME"]
	assert.Equal(t, "default-test-pvc@test-host", username)
}

func TestKopiaUserManager_EnsureUser_ExistingSecret(t *testing.T) {
	// NOTE: When the secret already exists, EnsureUser still calls createUserOnServer
	// to ensure the user exists on the server. However, since we don't have a real
	// Kubernetes clientset, the exec will fail. But the error is logged not returned,
	// so the function should succeed and return the secret name.
	//
	// BUT: When a pod IS found, it tries to exec which requires a non-nil Clientset.
	// To avoid the panic, we don't create a server pod in this test.

	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
			UID:       "test-uid",
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
			Hostname: "test-host",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
		Status: backupv1alpha1.KopiaRepositoryStatus{
			ServerReady:        true,
			TLSCertFingerprint: "ABC123",
		},
	}

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kopia-backup-user-default-test-pvc",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"KOPIA_SERVER_USERNAME": []byte("default-test-pvc@test-host"),
			"KOPIA_SERVER_PASSWORD": []byte("existing-password"),
		},
	}

	// NO server pod - this will cause createUserOnServer to return ServerNotReadyError
	// which is returned from EnsureUser when secret already exists
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSecret).
		Build()

	manager := &KopiaUserManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()
	_, err := manager.EnsureUser(ctx, backup, repo)

	// Should return ServerNotReadyError since no server pod exists
	var serverNotReady *ServerNotReadyError
	assert.True(t, errors.As(err, &serverNotReady))

	// Verify secret was not modified
	secret := &corev1.Secret{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-backup-user-default-test-pvc",
		Namespace: "default",
	}, secret)
	require.NoError(t, err)
	assert.Equal(t, "existing-password", string(secret.Data["KOPIA_SERVER_PASSWORD"]))
}

func TestKopiaUserManager_DeleteUser(t *testing.T) {
	// NOTE: DeleteUser first calls deleteUserFromServer which would try to exec
	// into the server pod. Since we don't have a real Kubernetes clientset,
	// we don't create a server pod to avoid the exec path.
	// The deleteUserFromServer error is logged but not returned, so the
	// secret deletion should still succeed.

	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

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
			Hostname: "test-host",
			Server: backupv1alpha1.KopiaServerSpec{
				Enabled: true,
			},
		},
	}

	existingSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kopia-backup-user-default-test-pvc",
			Namespace: "default",
		},
		Data: map[string][]byte{
			"KOPIA_SERVER_USERNAME": []byte("default-test-pvc@test-host"),
			"KOPIA_SERVER_PASSWORD": []byte("test-password"),
		},
	}

	// NO server pod - deleteUserFromServer will fail to find the pod but
	// the error is logged and the function continues to delete the secret
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSecret).
		Build()

	manager := &KopiaUserManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}

	ctx := context.Background()

	// Delete user - deleteUserFromServer will log an error but secret deletion should work
	err := manager.DeleteUser(ctx, backup, repo)
	require.NoError(t, err)

	// Verify secret was deleted
	secret := &corev1.Secret{}
	err = client.Get(ctx, types.NamespacedName{
		Name:      "kopia-backup-user-default-test-pvc",
		Namespace: "default",
	}, secret)
	require.Error(t, err) // Should not find the secret
}

func TestKopiaUserManager_GetServerPodName(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	repo := &backupv1alpha1.KopiaRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-repo",
			Namespace: "default",
		},
	}

	t.Run("pod not found", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := &KopiaUserManager{
			Client: client,
			Scheme: scheme,
			Log:    log,
		}

		ctx := context.Background()
		_, err := manager.getServerPodName(ctx, repo)

		require.Error(t, err)
		var serverNotReady *ServerNotReadyError
		assert.ErrorAs(t, err, &serverNotReady)
	})

	t.Run("pod found but container not ready", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kopia-server-test-repo-abc123",
				Namespace: "default",
				Labels: map[string]string{
					"app":                          "kopia-server",
					"app.kubernetes.io/name":       "kopia-server",
					"app.kubernetes.io/instance":   "test-repo",
					"app.kubernetes.io/managed-by": "kopia-operator",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "kopia-server",
						Ready: false, // Not ready
					},
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
		manager := &KopiaUserManager{
			Client: client,
			Scheme: scheme,
			Log:    log,
		}

		ctx := context.Background()
		_, err := manager.getServerPodName(ctx, repo)

		require.Error(t, err)
		var serverNotReady *ServerNotReadyError
		assert.ErrorAs(t, err, &serverNotReady)
	})

	t.Run("pod found and ready", func(t *testing.T) {
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kopia-server-test-repo-abc123",
				Namespace: "default",
				Labels: map[string]string{
					"app":                          "kopia-server",
					"app.kubernetes.io/name":       "kopia-server",
					"app.kubernetes.io/instance":   "test-repo",
					"app.kubernetes.io/managed-by": "kopia-operator",
				},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{
					{
						Name:  "kopia-server",
						Ready: true,
					},
				},
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
		manager := &KopiaUserManager{
			Client: client,
			Scheme: scheme,
			Log:    log,
		}

		ctx := context.Background()
		podName, err := manager.getServerPodName(ctx, repo)

		require.NoError(t, err)
		assert.Equal(t, "kopia-server-test-repo-abc123", podName)
	})
}

func TestKopiaUserManager_GetUserCredentials(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = backupv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	log := zap.New(zap.UseDevMode(true))

	backup := &backupv1alpha1.KopiaBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-backup",
			Namespace: "default",
		},
	}

	t.Run("secret not found", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		manager := &KopiaUserManager{
			Client: client,
			Scheme: scheme,
			Log:    log,
		}

		ctx := context.Background()
		_, _, err := manager.GetUserCredentials(ctx, backup)

		require.Error(t, err)
	})

	t.Run("credentials found", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-backup-kopia-creds",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"username": []byte("test-user"),
				"password": []byte("test-password"),
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
		manager := &KopiaUserManager{
			Client: client,
			Scheme: scheme,
			Log:    log,
		}

		ctx := context.Background()
		username, password, err := manager.GetUserCredentials(ctx, backup)

		require.NoError(t, err)
		assert.Equal(t, "test-user", username)
		assert.Equal(t, "test-password", password)
	})
}
