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
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// KopiaUserManager manages users on the Kopia Server
type KopiaUserManager struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// NewKopiaUserManager creates a new KopiaUserManager
func NewKopiaUserManager(client client.Client, scheme *runtime.Scheme, log logr.Logger) *KopiaUserManager {
	return &KopiaUserManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}
}

// EnsureUser ensures a user exists for the backup on the Kopia Server
// Returns the secret name containing the credentials
func (m *KopiaUserManager) EnsureUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) (string, error) {
	// Generate username from namespace and PVC name
	username := fmt.Sprintf("%s-%s", backup.Namespace, backup.Spec.PVCName)
	secretName := fmt.Sprintf("%s-kopia-creds", backup.Name)

	m.Log.Info("Ensuring Kopia user", "username", username, "backup", backup.Name)

	// Check if credentials secret already exists
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: backup.Namespace,
	}, secret)

	if err != nil {
		if !errors.IsNotFound(err) {
			return "", err
		}

		// Create new credentials
		password, err := m.generateSecurePassword(32)
		if err != nil {
			return "", fmt.Errorf("failed to generate password: %w", err)
		}

		// Create secret with credentials
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: backup.Namespace,
				Labels: map[string]string{
					"app":                          "kopia-backup",
					"kopia-backup":                 backup.Name,
					"app.kubernetes.io/name":       "kopia-backup",
					"app.kubernetes.io/instance":   backup.Name,
					"app.kubernetes.io/managed-by": "kopia-operator",
				},
			},
			StringData: map[string]string{
				"username": username,
				"password": password,
			},
		}

		if err := ctrl.SetControllerReference(backup, secret, m.Scheme); err != nil {
			return "", err
		}

		m.Log.Info("Creating user credentials secret", "secret", secretName, "username", username)
		if err := m.Client.Create(ctx, secret); err != nil {
			return "", err
		}

		// TODO: Actually create the user on the Kopia Server via API
		// For now, we're just creating the secret. The user creation
		// will happen when the backup pod runs and connects to the server
		// In a future iteration, we'll use the Kopia Server API to create users

		return secretName, nil
	}

	// Secret already exists
	m.Log.Info("User credentials secret already exists", "secret", secretName)
	return secretName, nil
}

// DeleteUser deletes a user from the Kopia Server
func (m *KopiaUserManager) DeleteUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) error {
	username := fmt.Sprintf("%s-%s", backup.Namespace, backup.Spec.PVCName)
	secretName := fmt.Sprintf("%s-kopia-creds", backup.Name)

	m.Log.Info("Deleting Kopia user", "username", username, "backup", backup.Name)

	// Delete the credentials secret
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: backup.Namespace,
	}, secret)

	if err != nil {
		if errors.IsNotFound(err) {
			// Already deleted
			return nil
		}
		return err
	}

	m.Log.Info("Deleting user credentials secret", "secret", secretName)
	if err := m.Client.Delete(ctx, secret); err != nil {
		return err
	}

	// TODO: Actually delete the user from the Kopia Server via API
	// For now, we're just deleting the secret

	return nil
}

// GetUserCredentials retrieves the username and password for a backup
func (m *KopiaUserManager) GetUserCredentials(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
) (username string, password string, err error) {
	secretName := fmt.Sprintf("%s-kopia-creds", backup.Name)

	secret := &corev1.Secret{}
	err = m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: backup.Namespace,
	}, secret)

	if err != nil {
		return "", "", err
	}

	username = string(secret.Data["username"])
	password = string(secret.Data["password"])

	return username, password, nil
}

// generateSecurePassword generates a cryptographically secure random password
func (m *KopiaUserManager) generateSecurePassword(length int) (string, error) {
	// Generate random bytes
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	// Encode to base64 for a readable password
	// This will be slightly longer than the requested length
	password := base64.URLEncoding.EncodeToString(bytes)

	// Trim to requested length
	if len(password) > length {
		password = password[:length]
	}

	return password, nil
}
