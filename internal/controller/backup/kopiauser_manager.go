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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// ServerNotReadyError indicates the Kopia server is not ready yet
type ServerNotReadyError struct {
	Message string
}

func (e *ServerNotReadyError) Error() string {
	return e.Message
}

// KopiaUserManager manages users on the Kopia Server
type KopiaUserManager struct {
	Client     client.Client
	Scheme     *runtime.Scheme
	Log        logr.Logger
	RestConfig *rest.Config
	Clientset  *kubernetes.Clientset
}

// NewKopiaUserManager creates a new KopiaUserManager
func NewKopiaUserManager(client client.Client, scheme *runtime.Scheme, log logr.Logger, restConfig *rest.Config) (*KopiaUserManager, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}

	return &KopiaUserManager{
		Client:     client,
		Scheme:     scheme,
		Log:        log,
		RestConfig: restConfig,
		Clientset:  clientset,
	}, nil
}

// EnsureUser ensures a user exists for the backup on the Kopia Server
// Returns the secret name containing the credentials
func (m *KopiaUserManager) EnsureUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) (string, error) {
	// Generate username from namespace and PVC name
	username := fmt.Sprintf("%s-%s@%s", backup.Namespace, backup.Spec.PVCName, repo.Spec.Hostname)
	secretName := fmt.Sprintf("kopia-backup-user-%s-%s", backup.Namespace, backup.Spec.PVCName)

	m.Log.Info("Ensuring Kopia user", "username", username, "backup", backup.Name)

	// Check if credentials secret already exists
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: backup.Namespace,
	}, secret)

	if err != nil {
		if !apierrors.IsNotFound(err) {
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
				"KOPIA_SERVER_USERNAME": username,
				"KOPIA_SERVER_PASSWORD": password,
			},
		}

		if err := ctrl.SetControllerReference(backup, secret, m.Scheme); err != nil {
			return "", err
		}

		m.Log.Info("Creating user credentials secret", "secret", secretName, "username", username)
		if err := m.Client.Create(ctx, secret); err != nil {
			return "", err
		}

		// Create the user on the Kopia server
		if err := m.createUserOnServer(ctx, repo, username, password); err != nil {
			m.Log.Error(err, "Failed to create user on Kopia server, will retry")
			// Don't fail - the user will be created by the init container if needed
		}

		return secretName, nil
	}

	// Secret already exists - ensure user exists on server
	m.Log.Info("User credentials secret already exists", "secret", secretName)

	// Get username and password from existing secret
	existingUsername := string(secret.Data["KOPIA_SERVER_USERNAME"])
	existingPassword := string(secret.Data["KOPIA_SERVER_PASSWORD"])

	// Ensure user exists on server
	if err := m.createUserOnServer(ctx, repo, existingUsername, existingPassword); err != nil {
		m.Log.Error(err, "Failed to ensure user on Kopia server")
		// Don't fail - the user might already exist
	}

	return secretName, nil
}

// DeleteUser deletes a user from the Kopia Server
func (m *KopiaUserManager) DeleteUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) error {
	username := fmt.Sprintf("%s-%s@%s", backup.Namespace, backup.Spec.PVCName, repo.Spec.Hostname)
	secretName := fmt.Sprintf("kopia-backup-user-%s-%s", backup.Namespace, backup.Spec.PVCName)

	m.Log.Info("Deleting Kopia user", "username", username, "backup", backup.Name)

	// Delete the user from the Kopia server
	if err := m.deleteUserFromServer(ctx, repo, username); err != nil {
		m.Log.Error(err, "Failed to delete user from Kopia server")
		// Continue with secret deletion even if server deletion fails
	}

	// Delete the credentials secret
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: backup.Namespace,
	}, secret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Already deleted
			return nil
		}
		return err
	}

	m.Log.Info("Deleting user credentials secret", "secret", secretName)
	if err := m.Client.Delete(ctx, secret); err != nil {
		return err
	}

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

// createUserOnServer creates a user on the Kopia server by executing a command in the server pod
func (m *KopiaUserManager) createUserOnServer(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
	username string,
	password string,
) error {
	// Find the actual server pod
	podName, err := m.getServerPodName(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to find server pod: %w", err)
	}

	// Get admin password for server authentication
	adminPassword, err := m.getServerAdminPassword(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to get admin password: %w", err)
	}

	// Build the command to create the user
	cmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			# Set admin credentials
			export KOPIA_SERVER_USERNAME=admin
			export KOPIA_SERVER_PASSWORD='%s'

			# Create the user (this will fail if user already exists, which is okay)
			kopia server user add '%s' --user-password='%s' 2>&1 || echo "User may already exist"

			# Set ACLs for the user
			kopia server user set '%s' --set-access=FULL 2>&1 || true
		`, adminPassword, username, password, username),
	}

	// Execute the command in the server pod
	stdout, stderr, err := m.execInPod(ctx, repo.Namespace, podName, "kopia-server", cmd)
	if err != nil {
		m.Log.Error(err, "Failed to create user on server",
			"stdout", stdout,
			"stderr", stderr,
			"username", username)
		// Check if this is a container not ready error
		if strings.Contains(err.Error(), "container not found") ||
			strings.Contains(err.Error(), "unable to upgrade connection") {
			return &ServerNotReadyError{
				Message: fmt.Sprintf("kopia-server container not ready yet: %v", err),
			}
		}
		return fmt.Errorf("failed to execute user creation command: %w", err)
	}

	m.Log.Info("Created user on Kopia server",
		"username", username,
		"stdout", stdout)

	return nil
}

// deleteUserFromServer deletes a user from the Kopia server by executing a command in the server pod
func (m *KopiaUserManager) deleteUserFromServer(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
	username string,
) error {
	// Find the actual server pod
	podName, err := m.getServerPodName(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to find server pod: %w", err)
	}

	// Get admin password for server authentication
	adminPassword, err := m.getServerAdminPassword(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to get admin password: %w", err)
	}

	// Build the command to delete the user
	cmd := []string{
		"/bin/sh",
		"-c",
		fmt.Sprintf(`
			# Set admin credentials
			export KOPIA_SERVER_USERNAME=admin
			export KOPIA_SERVER_PASSWORD='%s'

			# Delete the user (ignore errors if user doesn't exist)
			kopia server user delete '%s' 2>&1 || echo "User may not exist"
		`, adminPassword, username),
	}

	// Execute the command in the server pod
	stdout, stderr, err := m.execInPod(ctx, repo.Namespace, podName, "kopia-server", cmd)
	if err != nil {
		m.Log.Error(err, "Failed to delete user from server",
			"stdout", stdout,
			"stderr", stderr,
			"username", username)
		return fmt.Errorf("failed to execute user deletion command: %w", err)
	}

	m.Log.Info("Deleted user from Kopia server",
		"username", username,
		"stdout", stdout)

	return nil
}

// getServerPodName finds the running Kopia server pod for the given repository
func (m *KopiaUserManager) getServerPodName(ctx context.Context, repo *backupv1alpha1.KopiaRepository) (string, error) {
	// List pods with the kopia-server label
	podList := &corev1.PodList{}
	labels := map[string]string{
		"app":                          "kopia-server",
		"app.kubernetes.io/name":       "kopia-server",
		"app.kubernetes.io/instance":   repo.Name,
		"app.kubernetes.io/managed-by": "kopia-operator",
	}

	listOpts := []client.ListOption{
		client.InNamespace(repo.Namespace),
		client.MatchingLabels(labels),
	}

	if err := m.Client.List(ctx, podList, listOpts...); err != nil {
		return "", fmt.Errorf("failed to list server pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return "", &ServerNotReadyError{
			Message: fmt.Sprintf("no server pod found for repository %s in namespace %s - server may still be starting", repo.Name, repo.Namespace),
		}
	}

	// Find the first pod with a ready kopia-server container
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			// Check if the kopia-server container is ready
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Name == "kopia-server" && containerStatus.Ready {
					return pod.Name, nil
				}
			}
		}
	}

	// No pod with a ready container found
	return "", &ServerNotReadyError{
		Message: fmt.Sprintf("kopia-server container not ready yet for repository %s in namespace %s", repo.Name, repo.Namespace),
	}
}

// getServerAdminPassword retrieves the admin password for the Kopia server
func (m *KopiaUserManager) getServerAdminPassword(ctx context.Context, repo *backupv1alpha1.KopiaRepository) (string, error) {
	var secretName string
	var secretKey string

	if repo.Spec.Server.ServerAdminPasswordExistingSecret != "" {
		// Parse the existing secret reference in format "secretname/key"
		parts := strings.SplitN(repo.Spec.Server.ServerAdminPasswordExistingSecret, "/", 2)
		secretName = parts[0]
		if len(parts) > 1 {
			secretKey = parts[1]
		} else {
			secretKey = "password"
		}
	} else {
		// Use auto-generated secret
		secretName = fmt.Sprintf("kopia-server-admin-%s", repo.Name)
		secretKey = "password"
	}

	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: repo.Namespace,
	}, secret)
	if err != nil {
		return "", fmt.Errorf("failed to get admin password secret: %w", err)
	}

	password, ok := secret.Data[secretKey]
	if !ok {
		return "", fmt.Errorf("password key %s not found in secret %s", secretKey, secretName)
	}

	return string(password), nil
}

// execInPod executes a command in a pod and returns stdout, stderr, and error
func (m *KopiaUserManager) execInPod(ctx context.Context, namespace, podName, containerName string, cmd []string) (string, string, error) {
	req := m.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(m.RestConfig, "POST", req.URL())
	if err != nil {
		return "", "", fmt.Errorf("failed to create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})

	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}
