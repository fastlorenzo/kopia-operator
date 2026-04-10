package user

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

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
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/kopia"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

// PodExecutor is a function type for executing commands in pods.
// It can be overridden in tests to avoid needing a real cluster.
type PodExecutor func(ctx context.Context, namespace, podName, containerName string, cmd []string) (string, string, error)

// KopiaUserManager manages users on the Kopia Server.
type KopiaUserManager struct {
	Client      client.Client
	Scheme      *runtime.Scheme
	RestConfig  *rest.Config
	Clientset   *kubernetes.Clientset
	podExecutor PodExecutor
}

// NewKopiaUserManager creates a new KopiaUserManager.
func NewKopiaUserManager(c client.Client, s *runtime.Scheme, restConfig *rest.Config) (*KopiaUserManager, error) {
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset: %w", err)
	}
	mgr := &KopiaUserManager{
		Client:     c,
		Scheme:     s,
		RestConfig: restConfig,
		Clientset:  clientset,
	}
	mgr.podExecutor = mgr.execInPod
	return mgr, nil
}

// EnsureUser ensures a user exists for the backup on the Kopia Server.
// Returns the secret name containing the credentials.
func (m *KopiaUserManager) EnsureUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) (string, error) {
	logger := log.FromContext(ctx)

	username := naming.Username(backup.Namespace, backup.Spec.PVCName, repo.Spec.Hostname)
	secretName := naming.UserSecretName(backup.Namespace, backup.Spec.PVCName)

	logger.Info("Ensuring Kopia user", "username", username, "backup", backup.Name)

	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: backup.Namespace}, secret)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("failed to get user credentials secret: %w", err)
		}

		password, err := generateSecurePassword(32)
		if err != nil {
			return "", fmt.Errorf("failed to generate password: %w", err)
		}

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
			return "", fmt.Errorf("failed to set controller reference on user secret: %w", err)
		}

		logger.Info("Creating user credentials secret", "secret", secretName, "username", username)
		if err := m.Client.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("failed to create user credentials secret: %w", err)
		}

		if err := m.createUserOnServer(ctx, repo, username, password); err != nil {
			var serverNotReady *kopia.ServerNotReadyError
			if errors.As(err, &serverNotReady) {
				logger.Info("Server not ready, will requeue", "error", err.Error())
				return "", err
			}
			return "", fmt.Errorf("failed to create user on Kopia server: %w", err)
		}

		return secretName, nil
	}

	logger.Info("User credentials secret already exists", "secret", secretName)
	existingUsername, ok := secret.Data["KOPIA_SERVER_USERNAME"]
	if !ok {
		return "", fmt.Errorf("secret %q missing required key KOPIA_SERVER_USERNAME", secretName)
	}
	existingPassword, ok := secret.Data["KOPIA_SERVER_PASSWORD"]
	if !ok {
		return "", fmt.Errorf("secret %q missing required key KOPIA_SERVER_PASSWORD", secretName)
	}

	if err := m.createUserOnServer(ctx, repo, string(existingUsername), string(existingPassword)); err != nil {
		var serverNotReady *kopia.ServerNotReadyError
		if errors.As(err, &serverNotReady) {
			logger.Info("Server not ready, will requeue", "error", err.Error())
			return "", err
		}
		return "", fmt.Errorf("failed to ensure user on Kopia server: %w", err)
	}

	return secretName, nil
}

// DeleteUser deletes a user from the Kopia Server.
func (m *KopiaUserManager) DeleteUser(
	ctx context.Context,
	backup *backupv1alpha1.KopiaBackup,
	repo *backupv1alpha1.KopiaRepository,
) error {
	logger := log.FromContext(ctx)

	username := naming.Username(backup.Namespace, backup.Spec.PVCName, repo.Spec.Hostname)
	secretName := naming.UserSecretName(backup.Namespace, backup.Spec.PVCName)

	logger.Info("Deleting Kopia user", "username", username, "backup", backup.Name)

	if err := m.deleteUserFromServer(ctx, repo, username); err != nil {
		logger.Error(err, "Failed to delete user from Kopia server")
	}

	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: backup.Namespace}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get user credentials secret: %w", err)
	}

	logger.Info("Deleting user credentials secret", "secret", secretName)
	return m.Client.Delete(ctx, secret)
}

// generateSecurePassword generates a cryptographically secure random password.
func generateSecurePassword(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	password := base64.URLEncoding.EncodeToString(b)
	if len(password) > length {
		password = password[:length]
	}
	return password, nil
}

// createUserOnServer creates a user on the Kopia server via kubectl exec.
func (m *KopiaUserManager) createUserOnServer(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
	username string,
	password string,
) error {
	logger := log.FromContext(ctx)

	podName, err := m.getServerPodName(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to find server pod: %w", err)
	}

	cmd := buildCreateUserCommand(username, password, repo.Status.TLSCertFingerprint)

	stdout, stderr, err := m.podExecutor(ctx, repo.Namespace, podName, "kopia-server", cmd)
	if err != nil {
		logger.Error(err, "Failed to create user on server", "stdout", stdout, "stderr", stderr, "username", username)
		if strings.Contains(err.Error(), "container not found") ||
			strings.Contains(err.Error(), "unable to upgrade connection") {
			return &kopia.ServerNotReadyError{
				Message: fmt.Sprintf("kopia-server container not ready yet: %v", err),
			}
		}
		return fmt.Errorf("failed to execute user creation command: %w", err)
	}

	logger.Info("Created user on Kopia server", "username", username, "stdout", stdout)
	return nil
}

// deleteUserFromServer deletes a user from the Kopia server via kubectl exec.
func (m *KopiaUserManager) deleteUserFromServer(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
	username string,
) error {
	logger := log.FromContext(ctx)

	podName, err := m.getServerPodName(ctx, repo)
	if err != nil {
		return fmt.Errorf("failed to find server pod: %w", err)
	}

	cmd := buildDeleteUserCommand(username)

	stdout, stderr, err := m.podExecutor(ctx, repo.Namespace, podName, "kopia-server", cmd)
	if err != nil {
		logger.Error(err, "Failed to delete user from server", "stdout", stdout, "stderr", stderr, "username", username)
		return fmt.Errorf("failed to execute user deletion command: %w", err)
	}

	logger.Info("Deleted user from Kopia server", "username", username, "stdout", stdout)
	return nil
}

// getServerPodName finds the running Kopia server pod for the given repository.
func (m *KopiaUserManager) getServerPodName(ctx context.Context, repo *backupv1alpha1.KopiaRepository) (string, error) {
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(repo.Namespace),
		client.MatchingLabels(naming.ServerLabels(repo.Name)),
	}

	if err := m.Client.List(ctx, podList, listOpts...); err != nil {
		return "", fmt.Errorf("failed to list server pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return "", &kopia.ServerNotReadyError{
			Message: fmt.Sprintf("no server pod found for repository %s in namespace %s", repo.Name, repo.Namespace),
		}
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			for _, cs := range pod.Status.ContainerStatuses {
				if cs.Name == "kopia-server" && cs.Ready {
					return pod.Name, nil
				}
			}
		}
	}

	return "", &kopia.ServerNotReadyError{
		Message: fmt.Sprintf("kopia-server container not ready yet for repository %s in namespace %s", repo.Name, repo.Namespace),
	}
}

// execInPod executes a command in a pod and returns stdout, stderr, and error.
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

// buildCreateUserCommand constructs the exec command for creating or updating a
// Kopia server user. Credentials are passed as positional arguments to /bin/sh
// rather than interpolated into the shell script, preventing shell injection.
func buildCreateUserCommand(username, password, certFingerprint string) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e
USERNAME="$1"
PASSWORD="$2"
CERT_FP="$3"
echo "Checking if user $USERNAME exists..."
USER_LIST=$(kopia server user list 2>&1) || true
if echo "$USER_LIST" | grep -qF "$USERNAME"; then
  echo "User $USERNAME already exists, updating password..."
  kopia server user set "$USERNAME" --user-password="$PASSWORD" 2>&1
else
  echo "Creating user: $USERNAME"
  kopia server user add "$USERNAME" --user-password="$PASSWORD" 2>&1
fi
echo "Refreshing server..."
kopia server refresh --server-control-username=admin --server-control-password="${KOPIA_SERVER_PASSWORD}" --address=https://127.0.0.1:51515 --server-cert-fingerprint="$CERT_FP" 2>&1 || echo "Server refresh failed"`,
		"_", username, password, certFingerprint,
	}
}

// buildDeleteUserCommand constructs the exec command for deleting a Kopia server
// user. The username is passed as a positional argument to prevent shell injection.
func buildDeleteUserCommand(username string) []string {
	return []string{
		"/bin/sh", "-c",
		`kopia server user delete "$1" 2>&1 || echo "User may not exist"`,
		"_", username,
	}
}
