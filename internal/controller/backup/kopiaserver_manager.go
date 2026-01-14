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
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
)

// KopiaServerManager manages the lifecycle of Kopia Server deployments
type KopiaServerManager struct {
	Client client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// NewKopiaServerManager creates a new KopiaServerManager
func NewKopiaServerManager(client client.Client, scheme *runtime.Scheme, log logr.Logger) *KopiaServerManager {
	return &KopiaServerManager{
		Client: client,
		Scheme: scheme,
		Log:    log,
	}
}

// EnsureServerDeployment creates or updates the Kopia Server deployment
func (m *KopiaServerManager) EnsureServerDeployment(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (*appsv1.Deployment, error) {
	deploymentName := fmt.Sprintf("kopia-server-%s", repo.Name)

	deployment := &appsv1.Deployment{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: repo.Namespace,
	}, deployment)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		// Create new deployment
		deployment = m.constructServerDeployment(repo, deploymentName)
		if err := ctrl.SetControllerReference(repo, deployment, m.Scheme); err != nil {
			return nil, err
		}
		m.Log.Info("Creating Kopia Server Deployment", "name", deploymentName)
		if err := m.Client.Create(ctx, deployment); err != nil {
			return nil, err
		}
		return deployment, nil
	}

	// Update existing deployment if needed
	desiredDeployment := m.constructServerDeployment(repo, deploymentName)
	deployment.Spec = desiredDeployment.Spec
	m.Log.Info("Updating Kopia Server Deployment", "name", deploymentName)
	if err := m.Client.Update(ctx, deployment); err != nil {
		return nil, err
	}

	return deployment, nil
}

// EnsureServerService creates or updates the Service for the Kopia Server
func (m *KopiaServerManager) EnsureServerService(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (*corev1.Service, error) {
	serviceName := fmt.Sprintf("kopia-server-%s", repo.Name)

	service := &corev1.Service{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      serviceName,
		Namespace: repo.Namespace,
	}, service)

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		// Create new service
		service = m.constructServerService(repo, serviceName)
		if err := ctrl.SetControllerReference(repo, service, m.Scheme); err != nil {
			return nil, err
		}
		m.Log.Info("Creating Kopia Server Service", "name", serviceName)
		if err := m.Client.Create(ctx, service); err != nil {
			return nil, err
		}
		return service, nil
	}

	// Update existing service if needed
	desiredService := m.constructServerService(repo, serviceName)
	service.Spec.Type = desiredService.Spec.Type
	service.Spec.Ports = desiredService.Spec.Ports
	service.Spec.Selector = desiredService.Spec.Selector
	m.Log.Info("Updating Kopia Server Service", "name", serviceName)
	if err := m.Client.Update(ctx, service); err != nil {
		return nil, err
	}

	return service, nil
}

// GetServerURL returns the URL to connect to the server
func (m *KopiaServerManager) GetServerURL(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
	svc *corev1.Service,
) string {
	serviceName := fmt.Sprintf("kopia-server-%s", repo.Name)
	servicePort := int32(51515)
	if repo.Spec.Server.Exposure.ServicePort != 0 {
		servicePort = repo.Spec.Server.Exposure.ServicePort
	}

	// Kopia requires HTTPS for server connections
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:%d",
		serviceName, repo.Namespace, servicePort)
}

// IsServerReady checks if the server deployment is ready
func (m *KopiaServerManager) IsServerReady(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (bool, error) {
	deploymentName := fmt.Sprintf("kopia-server-%s", repo.Name)

	deployment := &appsv1.Deployment{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      deploymentName,
		Namespace: repo.Namespace,
	}, deployment)

	if err != nil {
		return false, err
	}

	// Check if deployment has the desired number of replicas ready
	if deployment.Status.ReadyReplicas == deployment.Status.Replicas &&
		deployment.Status.Replicas > 0 {
		return true, nil
	}

	return false, nil
}

// constructServerDeployment builds the Deployment spec for the Kopia Server
func (m *KopiaServerManager) constructServerDeployment(
	repo *backupv1alpha1.KopiaRepository,
	deploymentName string,
) *appsv1.Deployment {
	replicas := int32(1)
	if repo.Spec.Server.Replicas > 0 {
		replicas = repo.Spec.Server.Replicas
	}

	image := "ghcr.io/fastlorenzo/kopia:latest"
	if repo.Spec.Server.Image != "" {
		image = repo.Spec.Server.Image
	}

	labels := map[string]string{
		"app":                          "kopia-server",
		"kopia-repository":             repo.Name,
		"app.kubernetes.io/name":       "kopia-server",
		"app.kubernetes.io/instance":   repo.Name,
		"app.kubernetes.io/managed-by": "kopia-operator",
	}

	// Determine TLS secret name for fingerprint
	tlsSecretName := m.getTLSSecretName(repo)

	// Build container environment
	env := []corev1.EnvVar{
		{
			Name: "KOPIA_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: m.getRepositoryPasswordSecretKeyRef(repo),
			},
		},
		{
			Name:  "KOPIA_SERVER_USERNAME",
			Value: fmt.Sprintf("%s@%s", repo.Spec.Username, repo.Spec.Hostname),
		},
		{
			Name: "KOPIA_SERVER_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: m.getServerAdminPasswordSecretKeyRef(repo),
			},
		},
		{
			Name: "KOPIA_TLS_FINGERPRINT",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: tlsSecretName,
					},
					Key:      "fingerprint",
					Optional: func(b bool) *bool { return &b }(true),
				},
			},
		},
	}

	// Build volume mounts
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "cache",
			MountPath: "/cache",
		},
		{
			Name:      "config",
			MountPath: "/config",
		},
		{
			Name:      "tls",
			MountPath: "/tls",
			ReadOnly:  true,
		},
	}

	// Add storage volume mount based on storage type
	switch repo.Spec.StorageType {
	case storageTypeFilesystem:
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "repository",
			MountPath: "/repository",
		})
	case storageTypeSFTP:
		// Mount SFTP credentials secret
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "sftp-credentials",
			MountPath: "/sftp-creds",
			ReadOnly:  true,
		})
	}

	// Determine TLS secret name - remove duplicate declaration at line 279
	// tlsSecretName is already declared above

	// Build volumes
	volumes := []corev1.Volume{
		{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  tlsSecretName,
					DefaultMode: func(i int32) *int32 { return &i }(0400),
				},
			},
		},
	}

	// Add storage volume based on storage type
	switch repo.Spec.StorageType {
	case storageTypeFilesystem:
		volumes = append(volumes, m.constructStorageVolume(repo))
	case storageTypeSFTP:
		// Add SFTP credentials secret volume
		volumes = append(volumes, corev1.Volume{
			Name: "sftp-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  repo.Spec.SFTPOptions.CredentialsSecret,
					DefaultMode: func(i int32) *int32 { return &i }(0600),
				},
			},
		})
	}

	// Build server command
	serverCmd := m.constructServerCommand(repo)

	// Build container
	container := corev1.Container{
		Name:            "kopia-server",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{serverCmd},
		Env:             env,
		VolumeMounts:    volumeMounts,
		Ports: []corev1.ContainerPort{
			{
				Name:          "api",
				ContainerPort: 51515,
				Protocol:      corev1.ProtocolTCP,
			},
		},
		Resources: repo.Spec.Server.Resources,
	}

	// Configure probes - use TCP probe for liveness (simpler, just checks port is open)
	// and exec probe with certificate fingerprint file for readiness
	container.LivenessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: intstr.FromInt(51515),
			},
		},
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	// For readiness, we need to verify the server is actually responding
	// Use the fingerprint from env var (populated from TLS secret) or calculate from cert file as fallback
	readinessCmd := `FINGERPRINT="${KOPIA_TLS_FINGERPRINT:-$(openssl x509 -in /tls/tls.crt -noout -fingerprint -sha256 | cut -d= -f2 | tr -d ':')}" && kopia server status --address=https://127.0.0.1:51515 --server-username=admin --server-password="$KOPIA_SERVER_PASSWORD" --server-cert-fingerprint=$FINGERPRINT`

	container.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			Exec: &corev1.ExecAction{
				Command: []string{"/bin/sh", "-c", readinessCmd},
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       5,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}

	// If no resources specified, set defaults
	if container.Resources.Requests == nil {
		container.Resources.Requests = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256Mi"),
			corev1.ResourceCPU:    resource.MustParse("100m"),
		}
	}
	if container.Resources.Limits == nil {
		container.Resources.Limits = corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourceCPU:    resource.MustParse("1000m"),
		}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: repo.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}

	return deployment
}

// constructServerService builds the Service spec for the Kopia Server
func (m *KopiaServerManager) constructServerService(
	repo *backupv1alpha1.KopiaRepository,
	serviceName string,
) *corev1.Service {
	labels := map[string]string{
		"app":                          "kopia-server",
		"kopia-repository":             repo.Name,
		"app.kubernetes.io/name":       "kopia-server",
		"app.kubernetes.io/instance":   repo.Name,
		"app.kubernetes.io/managed-by": "kopia-operator",
	}

	serviceType := corev1.ServiceTypeClusterIP
	if repo.Spec.Server.Exposure.ServiceType != "" {
		serviceType = repo.Spec.Server.Exposure.ServiceType
	}

	servicePort := int32(51515)
	if repo.Spec.Server.Exposure.ServicePort != 0 {
		servicePort = repo.Spec.Server.Exposure.ServicePort
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: repo.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "api",
					Port:       servicePort,
					TargetPort: intstr.FromInt(51515),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	return service
}

// constructStorageVolume creates the volume for the repository storage
func (m *KopiaServerManager) constructStorageVolume(repo *backupv1alpha1.KopiaRepository) corev1.Volume {
	volume := corev1.Volume{
		Name: "repository",
	}

	if repo.Spec.FileSystemOptions.NFSServer != "" {
		// NFS volume
		volume.VolumeSource = corev1.VolumeSource{
			NFS: &corev1.NFSVolumeSource{
				Server: repo.Spec.FileSystemOptions.NFSServer,
				Path:   repo.Spec.FileSystemOptions.NFSPath,
			},
		}
	} else {
		// HostPath volume (for testing or single-node clusters)
		hostPathType := corev1.HostPathDirectoryOrCreate
		volume.VolumeSource = corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: repo.Spec.FileSystemOptions.Path,
				Type: &hostPathType,
			},
		}
	}

	return volume
}

// buildCacheFlags generates Kopia cache configuration flags from repository spec
func buildCacheFlags(caching backupv1alpha1.KopiaRepositoryCachingSpec) string {
	flags := ""

	if caching.CacheDirectory != "" {
		flags += fmt.Sprintf(" --cache-directory=%s", caching.CacheDirectory)
	}

	if caching.ContentCacheSizeBytes > 0 {
		flags += fmt.Sprintf(" --content-cache-size-mb=%d", caching.ContentCacheSizeBytes/(1024*1024))
	}

	if caching.ContentCacheSizeLimitBytes > 0 {
		flags += fmt.Sprintf(" --content-cache-size-limit-mb=%d", caching.ContentCacheSizeLimitBytes/(1024*1024))
	}

	if caching.MetadataCacheSizeBytes > 0 {
		flags += fmt.Sprintf(" --metadata-cache-size-mb=%d", caching.MetadataCacheSizeBytes/(1024*1024))
	}

	if caching.MetadataCacheSizeLimitBytes > 0 {
		flags += fmt.Sprintf(" --metadata-cache-size-limit-mb=%d", caching.MetadataCacheSizeLimitBytes/(1024*1024))
	}

	if caching.MaxListCacheDuration > 0 {
		flags += fmt.Sprintf(" --max-list-cache-duration=%ds", caching.MaxListCacheDuration)
	}

	if caching.MinMetadataSweepAge > 0 {
		flags += fmt.Sprintf(" --min-metadata-sweep-age=%ds", caching.MinMetadataSweepAge)
	}

	if caching.MinContentSweepAge > 0 {
		flags += fmt.Sprintf(" --min-content-sweep-age=%ds", caching.MinContentSweepAge)
	}

	if caching.MinIndexSweepAge > 0 {
		flags += fmt.Sprintf(" --min-index-sweep-age=%ds", caching.MinIndexSweepAge)
	}

	return flags
}

// constructServerCommand builds the command to start the Kopia Server
func (m *KopiaServerManager) constructServerCommand(repo *backupv1alpha1.KopiaRepository) string {
	// Build cache flags from repository spec
	cacheFlags := buildCacheFlags(repo.Spec.Caching)

	// Build repository connection string based on storage type
	var repoConnect string
	switch repo.Spec.StorageType {
	case storageTypeFilesystem:
		repoConnect = fmt.Sprintf("kopia repository connect filesystem --path=/repository --override-hostname=%s --override-username=%s%s",
			repo.Spec.Hostname, repo.Spec.Username, cacheFlags)
	case storageTypeSFTP:
		// Build SFTP connection command using direct configuration
		port := 22
		if repo.Spec.SFTPOptions.Port > 0 {
			port = repo.Spec.SFTPOptions.Port
		}

		sftpCmd := fmt.Sprintf("kopia repository connect sftp --host=%s --port=%d --path=%s",
			repo.Spec.SFTPOptions.Host,
			port,
			repo.Spec.SFTPOptions.Path)

		// Read credentials from mounted secret
		repoConnect = fmt.Sprintf(`
# Read SFTP credentials from secret
SFTP_USER=$(cat /sftp-creds/username 2>/dev/null || echo "")
SFTP_PASSWORD=$(cat /sftp-creds/password 2>/dev/null || echo "")
SFTP_KEY=$(cat /sftp-creds/keyData 2>/dev/null || echo "")

if [ -z "$SFTP_USER" ]; then
  echo "ERROR: SFTP username not found in secret"
  exit 1
fi

# Build connection command with credentials
SFTP_CMD="%s --username=$SFTP_USER"

# Use SSH key if provided, otherwise use password
if [ -n "$SFTP_KEY" ]; then
  echo "$SFTP_KEY" > /tmp/ssh_key
  chmod 600 /tmp/ssh_key
  SFTP_CMD="$SFTP_CMD --keyfile=/tmp/ssh_key"
elif [ -n "$SFTP_PASSWORD" ]; then
  SFTP_CMD="$SFTP_CMD --sftp-password=$SFTP_PASSWORD"
else
  echo "ERROR: Neither keyData nor password found in secret"
  exit 1
fi
`, sftpCmd)

		// Add known_hosts if provided
		if repo.Spec.SFTPOptions.KnownHostsData != "" {
			repoConnect += `
# Add known_hosts
echo "` + repo.Spec.SFTPOptions.KnownHostsData + `" > /tmp/known_hosts
SFTP_CMD="$SFTP_CMD --known-hosts=/tmp/known_hosts"
`
		}

		// Add external SSH options if configured
		if repo.Spec.SFTPOptions.ExternalSSH {
			sshCmd := "ssh"
			if repo.Spec.SFTPOptions.SSHCommand != "" {
				sshCmd = repo.Spec.SFTPOptions.SSHCommand
			}
			repoConnect += fmt.Sprintf(`
SFTP_CMD="$SFTP_CMD --external-ssh --ssh-command=%s"
`, sshCmd)
		}

		// Add override flags and execute
		repoConnect += fmt.Sprintf(`
SFTP_CMD="$SFTP_CMD --override-hostname=%s --override-username=%s%s"
eval "$SFTP_CMD"
`, repo.Spec.Hostname, repo.Spec.Username, cacheFlags)
	default:
		repoConnect = fmt.Sprintf("echo 'Unsupported storage type: %s' && exit 1", repo.Spec.StorageType)
	}

	// Server start command - always use TLS
	serverStart := "kopia server start --tls-cert-file=/tls/tls.crt --tls-key-file=/tls/tls.key --address=0.0.0.0:51515 --server-control-username=admin --server-control-password=\"${KOPIA_SERVER_PASSWORD}\""

	// Add extra args if specified
	if len(repo.Spec.Server.ExtraArgs) > 0 {
		for _, arg := range repo.Spec.Server.ExtraArgs {
			serverStart += " " + arg
		}
	}

	// Full command with repository connection and server start
	var cmd string

	// Admin user setup
	adminUserSetup := fmt.Sprintf(`
# Set up admin user
ADMIN_USER="%s@%s"
echo "Checking admin user: $ADMIN_USER"
kopia server user list | grep "$ADMIN_USER" >/dev/null 2>&1
if [ $? -ne 0 ]; then
  echo "Creating admin user: $ADMIN_USER"
  kopia server user add "$ADMIN_USER" --user-password="${KOPIA_PASSWORD}"
else
  echo "Updating admin user password: $ADMIN_USER"
  kopia server user set "$ADMIN_USER" --user-password="${KOPIA_PASSWORD}"
fi
`, repo.Spec.Username, repo.Spec.Hostname)

	switch repo.Spec.StorageType {
	case storageTypeFilesystem:
		cmd = fmt.Sprintf(`
set -e
echo "Connecting to repository..."
%s || {
  echo "Repository connection failed, attempting to create..."
  kopia repository create filesystem --path=/repository --override-hostname=%s --override-username=%s%s
}
echo "Setting up admin user..."
%s
echo "Starting Kopia Server..."
%s
`, repoConnect, repo.Spec.Hostname, repo.Spec.Username, cacheFlags, adminUserSetup, serverStart)
	case storageTypeSFTP:
		// For SFTP, support auto-creation
		port := 22
		if repo.Spec.SFTPOptions.Port > 0 {
			port = repo.Spec.SFTPOptions.Port
		}

		sftpCreateCmd := fmt.Sprintf("kopia repository create sftp --host=%s --port=%d --path=%s",
			repo.Spec.SFTPOptions.Host,
			port,
			repo.Spec.SFTPOptions.Path)

		createCmd := fmt.Sprintf(`# Build SFTP create command with credentials
SFTP_CREATE_CMD="%s --username=$SFTP_USER"

# Use SSH key if provided, otherwise use password
if [ -n "$SFTP_KEY" ]; then
  SFTP_CREATE_CMD="$SFTP_CREATE_CMD --keyfile=/tmp/ssh_key"
elif [ -n "$SFTP_PASSWORD" ]; then
  SFTP_CREATE_CMD="$SFTP_CREATE_CMD --sftp-password=$SFTP_PASSWORD"
fi
`, sftpCreateCmd)

		// Add known_hosts to create command if provided
		if repo.Spec.SFTPOptions.KnownHostsData != "" {
			createCmd += `SFTP_CREATE_CMD="$SFTP_CREATE_CMD --known-hosts=/tmp/known_hosts"
`
		}

		// Add external SSH options if configured
		if repo.Spec.SFTPOptions.ExternalSSH {
			sshCmd := "ssh"
			if repo.Spec.SFTPOptions.SSHCommand != "" {
				sshCmd = repo.Spec.SFTPOptions.SSHCommand
			}
			createCmd += fmt.Sprintf(`SFTP_CREATE_CMD="$SFTP_CREATE_CMD --external-ssh --ssh-command=%s"
`, sshCmd)
		}

		// Add override flags to create command
		createCmd += fmt.Sprintf(`SFTP_CREATE_CMD="$SFTP_CREATE_CMD --override-hostname=%s --override-username=%s%s"
`, repo.Spec.Hostname, repo.Spec.Username, cacheFlags)

		cmd = fmt.Sprintf(`
echo "Connecting to repository..."
%s
if [ $? -ne 0 ]; then
  echo "Repository connection failed, attempting to create..."
%s  eval "$SFTP_CREATE_CMD"
  if [ $? -eq 0 ]; then
    echo "Repository created successfully, reconnecting..."
    eval "$SFTP_CMD"
  else
    echo "Failed to create repository"
    exit 1
  fi
fi
set -e
echo "Starting Kopia Server..."
%s
%s
`, repoConnect, createCmd, adminUserSetup, serverStart)
	default:
		// For other storage types, don't auto-create - repository must exist
		cmd = fmt.Sprintf(`
set -e
echo "Connecting to repository..."
%s
echo "Starting Kopia Server..."
%s
%s
`, repoConnect, adminUserSetup, serverStart)
	}

	return cmd
}

// EnsureRepositoryPasswordSecret creates or updates the repository password secret
// Only creates a secret if RepositoryPassword is set in the spec
func (m *KopiaServerManager) EnsureRepositoryPasswordSecret(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) error {
	// Skip if using existing secret
	if repo.Spec.RepositoryPasswordExistingSecret != "" {
		return nil
	}

	// Skip if no password is set
	if repo.Spec.RepositoryPassword == "" {
		return fmt.Errorf("repository password must be set either via repositoryPassword or repositoryPasswordExistingSecret")
	}

	secretName := fmt.Sprintf("kopia-repo-%s", repo.Name)
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: repo.Namespace,
	}, secret)

	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: repo.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kopia-repository",
				"app.kubernetes.io/instance":   repo.Name,
				"app.kubernetes.io/managed-by": "kopia-operator",
			},
		},
		StringData: map[string]string{
			"password":       repo.Spec.RepositoryPassword,
			"KOPIA_PASSWORD": repo.Spec.RepositoryPassword,
		},
	}

	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Create new secret
		if err := ctrl.SetControllerReference(repo, desiredSecret, m.Scheme); err != nil {
			return err
		}
		m.Log.Info("Creating repository password Secret", "name", secretName)
		if err := m.Client.Create(ctx, desiredSecret); err != nil {
			return err
		}
		return nil
	}

	// Update existing secret if password changed
	secret.StringData = desiredSecret.StringData
	m.Log.Info("Updating repository password Secret", "name", secretName)
	if err := m.Client.Update(ctx, secret); err != nil {
		return err
	}

	return nil
}

// EnsureServerAdminPasswordSecret creates a secret for the server admin password if ServerAdminPassword is set
func (m *KopiaServerManager) EnsureServerAdminPasswordSecret(ctx context.Context, repo *backupv1alpha1.KopiaRepository) error {
	// Only create secret if ServerAdminPassword is set and no existing secret is specified
	if repo.Spec.Server.ServerAdminPassword == "" || repo.Spec.Server.ServerAdminPasswordExistingSecret != "" {
		return nil
	}

	secretName := fmt.Sprintf("kopia-server-admin-%s", repo.Name)

	// Check if secret already exists
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: repo.Namespace,
	}, secret)

	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: repo.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kopia-server-admin",
				"app.kubernetes.io/instance":   repo.Name,
				"app.kubernetes.io/managed-by": "kopia-operator",
			},
		},
		StringData: map[string]string{
			"password": repo.Spec.Server.ServerAdminPassword,
		},
	}

	// Set owner reference
	if err := ctrl.SetControllerReference(repo, desiredSecret, m.Scheme); err != nil {
		return err
	}

	// If secret doesn't exist, create it
	if apierrors.IsNotFound(err) {
		m.Log.Info("Creating server admin password Secret", "name", secretName)
		if err := m.Client.Create(ctx, desiredSecret); err != nil {
			return err
		}
		return nil
	}

	if err != nil {
		return err
	}

	// Update existing secret if password changed
	secret.StringData = desiredSecret.StringData
	m.Log.Info("Updating server admin password Secret", "name", secretName)
	if err := m.Client.Update(ctx, secret); err != nil {
		return err
	}

	return nil
}

// getRepositoryPasswordSecretKeyRef returns the secret key reference for the repository password
func (m *KopiaServerManager) getRepositoryPasswordSecretKeyRef(repo *backupv1alpha1.KopiaRepository) *corev1.SecretKeySelector {
	if repo.Spec.RepositoryPasswordExistingSecret != "" {
		return &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: repo.Spec.RepositoryPasswordExistingSecret,
			},
			Key: "KOPIA_PASSWORD",
		}
	}

	// Use default secret name (created by EnsureRepositoryPasswordSecret)
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{
			Name: fmt.Sprintf("kopia-repo-%s", repo.Name),
		},
		Key: "password",
	}
}

// getServerAdminPasswordSecretKeyRef returns the secret key reference for the server admin password
func (m *KopiaServerManager) getServerAdminPasswordSecretKeyRef(repo *backupv1alpha1.KopiaRepository) *corev1.SecretKeySelector {
	// If existing secret specified, use it
	if repo.Spec.Server.ServerAdminPasswordExistingSecret != "" {
		return &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: repo.Spec.Server.ServerAdminPasswordExistingSecret,
			},
			Key: "password",
		}
	}

	// If server admin password is set, use the same secret as repository password
	// (EnsureServerAdminPasswordSecret will create it separately if needed)
	if repo.Spec.Server.ServerAdminPassword != "" {
		return &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: fmt.Sprintf("kopia-server-admin-%s", repo.Name),
			},
			Key: "password",
		}
	}

	// Fall back to repository password
	return m.getRepositoryPasswordSecretKeyRef(repo)
}

// getTLSSecretName returns the name of the TLS secret for the server
func (m *KopiaServerManager) getTLSSecretName(repo *backupv1alpha1.KopiaRepository) string {
	// If user provided a secret, use it
	if repo.Spec.Server.TLS.SecretName != "" {
		return repo.Spec.Server.TLS.SecretName
	}
	// Use auto-generated secret name
	return fmt.Sprintf("kopia-server-tls-%s", repo.Name)
}

// EnsureTLSSecret ensures TLS certificates exist for the Kopia Server
// If user provides a secret name, it validates the secret exists
// Otherwise, it auto-generates a self-signed certificate
// Returns the SHA256 fingerprint of the certificate
func (m *KopiaServerManager) EnsureTLSSecret(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (string, error) {
	tlsSecretName := m.getTLSSecretName(repo)

	// Check if secret already exists
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{
		Name:      tlsSecretName,
		Namespace: repo.Namespace,
	}, secret)

	if err == nil {
		// Secret exists
		if repo.Spec.Server.TLS.SecretName != "" {
			// User-provided secret - validate it has required keys
			certPEM, ok := secret.Data["tls.crt"]
			if !ok {
				return "", fmt.Errorf("TLS secret %s missing 'tls.crt' key", tlsSecretName)
			}
			if _, ok := secret.Data["tls.key"]; !ok {
				return "", fmt.Errorf("TLS secret %s missing 'tls.key' key", tlsSecretName)
			}
			// Calculate fingerprint from user-provided cert
			fingerprint, err := m.calculateCertFingerprint(certPEM)
			if err != nil {
				return "", fmt.Errorf("failed to calculate fingerprint from user-provided cert: %w", err)
			}
			m.Log.Info("Using user-provided TLS secret", "name", tlsSecretName, "fingerprint", fingerprint)
			return fingerprint, nil
		}
		// Auto-generated secret already exists - get fingerprint
		if fingerprint, ok := secret.Data["fingerprint"]; ok {
			m.Log.Info("TLS secret already exists", "name", tlsSecretName, "fingerprint", string(fingerprint))
			return string(fingerprint), nil
		}
		// Fingerprint not stored, calculate from cert
		fingerprint, err := m.calculateCertFingerprint(secret.Data["tls.crt"])
		if err != nil {
			return "", fmt.Errorf("failed to calculate fingerprint: %w", err)
		}
		m.Log.Info("TLS secret already exists", "name", tlsSecretName, "fingerprint", fingerprint)
		return fingerprint, nil
	}

	if !apierrors.IsNotFound(err) {
		return "", err
	}

	// Secret doesn't exist
	if repo.Spec.Server.TLS.SecretName != "" {
		// User specified a secret but it doesn't exist
		return "", fmt.Errorf("TLS secret %s not found - please create the secret with 'tls.crt' and 'tls.key' keys", tlsSecretName)
	}

	// Auto-generate self-signed certificate
	m.Log.Info("Auto-generating TLS certificate", "name", tlsSecretName)

	serviceName := fmt.Sprintf("kopia-server-%s", repo.Name)
	servicePort := int32(51515)
	if repo.Spec.Server.Exposure.ServicePort != 0 {
		servicePort = repo.Spec.Server.Exposure.ServicePort
	}

	// Build DNS names for the certificate
	dnsNames := []string{
		serviceName,
		fmt.Sprintf("%s.%s", serviceName, repo.Namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, repo.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, repo.Namespace),
	}

	// Add any additional DNS names from spec
	if len(repo.Spec.Server.TLS.CertificateDNSNames) > 0 {
		dnsNames = append(dnsNames, repo.Spec.Server.TLS.CertificateDNSNames...)
	}

	// Common name
	commonName := repo.Spec.Server.TLS.CertificateCommonName
	if commonName == "" {
		commonName = fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, repo.Namespace)
	}

	// Generate certificate
	certPEM, keyPEM, err := m.generateSelfSignedCert(commonName, dnsNames, servicePort)
	if err != nil {
		return "", fmt.Errorf("failed to generate TLS certificate: %w", err)
	}

	// Calculate fingerprint
	fingerprint, err := m.calculateCertFingerprint(certPEM)
	if err != nil {
		return "", fmt.Errorf("failed to calculate fingerprint: %w", err)
	}

	// Create secret with fingerprint stored as additional key
	tlsSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tlsSecretName,
			Namespace: repo.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "kopia-server",
				"app.kubernetes.io/instance":   repo.Name,
				"app.kubernetes.io/managed-by": "kopia-operator",
			},
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			"tls.crt":     certPEM,
			"tls.key":     keyPEM,
			"fingerprint": []byte(fingerprint),
		},
	}

	if err := ctrl.SetControllerReference(repo, tlsSecret, m.Scheme); err != nil {
		return "", err
	}

	m.Log.Info("Creating TLS Secret", "name", tlsSecretName, "fingerprint", fingerprint)
	if err := m.Client.Create(ctx, tlsSecret); err != nil {
		return "", err
	}

	return fingerprint, nil
}

// calculateCertFingerprint calculates the SHA256 fingerprint of a PEM-encoded certificate
func (m *KopiaServerManager) calculateCertFingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Calculate SHA256 hash
	hash := sha256.Sum256(cert.Raw)

	// Format as uppercase hex without colons (Kopia's expected format)
	return fmt.Sprintf("%X", hash), nil
}

// generateSelfSignedCert generates a self-signed TLS certificate
func (m *KopiaServerManager) generateSelfSignedCert(commonName string, dnsNames []string, port int32) ([]byte, []byte, error) {
	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour * 10) // 10 years

	// Add localhost for probes that connect via 127.0.0.1
	allDNSNames := append([]string{"localhost"}, dnsNames...)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"kopia-operator"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              allDNSNames,
		// Include 127.0.0.1 for probes that connect to localhost
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Encode private key to PEM
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	return certPEM, keyPEM, nil
}
