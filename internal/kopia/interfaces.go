package kopia

import (
	"context"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// ServerManager manages the lifecycle of Kopia Server deployments.
// Methods return wrapped errors with context. IsServerReady returns
// (false, nil) when the deployment exists but is not yet available.
type ServerManager interface {
	// EnsureServerDeployment ensures the Kopia Server Deployment exists and is up-to-date.
	EnsureServerDeployment(ctx context.Context, repo *backupv1alpha1.KopiaRepository) error
	// EnsureServerService ensures the Kopia Server Service exists and is up-to-date.
	EnsureServerService(ctx context.Context, repo *backupv1alpha1.KopiaRepository) error
	// EnsureTLSSecret ensures TLS certificates exist for the Kopia Server.
	// Returns the SHA256 fingerprint of the certificate.
	EnsureTLSSecret(ctx context.Context, repo *backupv1alpha1.KopiaRepository) (string, error)
	// IsServerReady checks if the server deployment is ready.
	IsServerReady(ctx context.Context, repo *backupv1alpha1.KopiaRepository) (bool, error)
	// GetServerURL returns the in-cluster URL for the Kopia Server.
	GetServerURL(repo *backupv1alpha1.KopiaRepository) string
}

// UserManager manages users on the Kopia Server.
// EnsureUser may return a ServerNotReadyError (from the kopia package)
// when the server pod is not yet available. Callers should check with
// errors.As and requeue accordingly.
type UserManager interface {
	// EnsureUser ensures a user exists for the backup on the Kopia Server.
	// Returns the secret name containing the credentials.
	EnsureUser(ctx context.Context, backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) (string, error)
	// DeleteUser deletes a user from the Kopia Server.
	DeleteUser(ctx context.Context, backup *backupv1alpha1.KopiaBackup, repo *backupv1alpha1.KopiaRepository) error
}
