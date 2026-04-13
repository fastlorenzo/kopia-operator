package server

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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	backupv1alpha1 "github.com/fastlorenzo/kopia-operator/api/backup/v1alpha1"
	"github.com/fastlorenzo/kopia-operator/internal/naming"
)

// KopiaServerManager manages the lifecycle of Kopia Server deployments.
type KopiaServerManager struct {
	Client client.Client
	Scheme *runtime.Scheme
}

const (
	// defaultServerPort is the default port for the Kopia Server API.
	defaultServerPort = 51515

	// Probe configuration constants for the Kopia server container.
	livenessInitialDelay  = 30
	livenessPeriod        = 10
	readinessInitialDelay = 10
	readinessPeriod       = 5
	probeTimeout          = 5
	probeFailureThreshold = 3
)

// shellQuote wraps a value in single quotes with proper escaping for safe
// interpolation into shell scripts. Single quotes inside the value are escaped
// using the standard shell idiom: end quote, escaped quote, restart quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// NewKopiaServerManager creates a new KopiaServerManager.
func NewKopiaServerManager(client client.Client, scheme *runtime.Scheme) *KopiaServerManager {
	return &KopiaServerManager{
		Client: client,
		Scheme: scheme,
	}
}

// EnsureServerDeployment ensures the Kopia Server Deployment exists and is up-to-date.
func (m *KopiaServerManager) EnsureServerDeployment(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) error {
	logger := log.FromContext(ctx)
	deploymentName := naming.ServerDeploymentName(repo.Name)

	desired := m.constructServerDeployment(repo, deploymentName)
	if err := ctrl.SetControllerReference(repo, desired, m.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Deployment: %w", err)
	}

	existing := &appsv1.Deployment{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: repo.Namespace}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get Deployment %s: %w", deploymentName, err)
		}
		logger.Info("Creating Kopia Server Deployment", "name", deploymentName)
		return m.Client.Create(ctx, desired)
	}

	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return nil
	}
	existing.Spec = desired.Spec
	logger.Info("Updating Kopia Server Deployment", "name", deploymentName)
	return m.Client.Update(ctx, existing)
}

// EnsureServerService ensures the Kopia Server Service exists and is up-to-date.
func (m *KopiaServerManager) EnsureServerService(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) error {
	logger := log.FromContext(ctx)
	serviceName := naming.ServerServiceName(repo.Name)

	desired := m.constructServerService(repo, serviceName)
	if err := ctrl.SetControllerReference(repo, desired, m.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Service: %w", err)
	}

	existing := &corev1.Service{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: repo.Namespace}, existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get Service %s: %w", serviceName, err)
		}
		logger.Info("Creating Kopia Server Service", "name", serviceName)
		return m.Client.Create(ctx, desired)
	}

	existing.Spec.Ports = desired.Spec.Ports
	existing.Spec.Type = desired.Spec.Type
	existing.Spec.Selector = desired.Spec.Selector
	if equality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return nil
	}
	logger.Info("Updating Kopia Server Service", "name", serviceName)
	return m.Client.Update(ctx, existing)
}

// GetServerURL returns the in-cluster URL for the Kopia Server.
func (m *KopiaServerManager) GetServerURL(repo *backupv1alpha1.KopiaRepository) string {
	serviceName := naming.ServerServiceName(repo.Name)
	port := int32(defaultServerPort)
	if repo.Spec.Server.Exposure.ServicePort != 0 {
		port = repo.Spec.Server.Exposure.ServicePort
	}
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:%d", serviceName, repo.Namespace, port)
}

// IsServerReady checks if the server deployment is ready.
func (m *KopiaServerManager) IsServerReady(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (bool, error) {
	deploymentName := naming.ServerDeploymentName(repo.Name)
	deployment := &appsv1.Deployment{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: repo.Namespace}, deployment)
	if err != nil {
		return false, fmt.Errorf("failed to get Deployment %s: %w", deploymentName, err)
	}
	return deployment.Status.ReadyReplicas == deployment.Status.Replicas && deployment.Status.Replicas > 0, nil
}

// constructServerDeployment builds the Deployment spec for the Kopia Server.
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

	labels := naming.ServerLabels(repo.Name)
	tlsSecretName := m.getTLSSecretName(repo)

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
					LocalObjectReference: corev1.LocalObjectReference{Name: tlsSecretName},
					Key:                  "fingerprint",
					Optional:             ptr.To(true),
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: "cache", MountPath: "/cache"},
		{Name: "config", MountPath: "/config"},
		{Name: "tmp", MountPath: "/tmp"},
		{Name: "tls", MountPath: "/tls", ReadOnly: true},
	}

	volumes := []corev1.Volume{
		{Name: "cache", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "config", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  tlsSecretName,
					DefaultMode: ptr.To(int32(0400)),
				},
			},
		},
	}

	switch repo.Spec.StorageType {
	case backupv1alpha1.StorageTypeFilesystem:
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "repository", MountPath: "/repository"})
		volumes = append(volumes, m.constructStorageVolume(repo))
	case backupv1alpha1.StorageTypeSFTP:
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name: "sftp-credentials", MountPath: "/sftp-creds", ReadOnly: true,
		})
		volumes = append(volumes, corev1.Volume{
			Name: "sftp-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  repo.Spec.SFTPOptions.CredentialsSecret,
					DefaultMode: ptr.To(int32(0600)),
				},
			},
		})
	}

	serverCmd := m.constructServerCommand(repo)

	container := corev1.Container{
		Name:            "kopia-server",
		Image:           image,
		ImagePullPolicy: corev1.PullAlways,
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{serverCmd},
		Env:             env,
		VolumeMounts:    volumeMounts,
		Ports: []corev1.ContainerPort{
			{Name: "api", ContainerPort: defaultServerPort, Protocol: corev1.ProtocolTCP},
		},
		Resources: repo.Spec.Server.Resources,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(defaultServerPort)},
			},
			InitialDelaySeconds: livenessInitialDelay,
			PeriodSeconds:       livenessPeriod,
			TimeoutSeconds:      probeTimeout,
			FailureThreshold:    probeFailureThreshold,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "-c",
						`FINGERPRINT="${KOPIA_TLS_FINGERPRINT:-$(openssl x509 -in /tls/tls.crt -noout -fingerprint -sha256 | cut -d= -f2 | tr -d ':')}" && ` +
							fmt.Sprintf(`kopia server status --address=https://127.0.0.1:%d --server-username=admin --server-password="$KOPIA_SERVER_PASSWORD" --server-cert-fingerprint=$FINGERPRINT`, defaultServerPort),
					},
				},
			},
			InitialDelaySeconds: readinessInitialDelay,
			PeriodSeconds:       readinessPeriod,
			TimeoutSeconds:      probeTimeout,
			FailureThreshold:    probeFailureThreshold,
		},
	}

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

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: repo.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}
}

// constructServerService builds the Service spec for the Kopia Server.
func (m *KopiaServerManager) constructServerService(
	repo *backupv1alpha1.KopiaRepository,
	serviceName string,
) *corev1.Service {
	labels := naming.ServerLabels(repo.Name)

	serviceType := corev1.ServiceTypeClusterIP
	if repo.Spec.Server.Exposure.ServiceType != "" {
		serviceType = repo.Spec.Server.Exposure.ServiceType
	}

	servicePort := int32(defaultServerPort)
	if repo.Spec.Server.Exposure.ServicePort != 0 {
		servicePort = repo.Spec.Server.Exposure.ServicePort
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: repo.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "api", Port: servicePort, TargetPort: intstr.FromInt(defaultServerPort), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}

// constructStorageVolume creates the volume for the repository storage.
func (m *KopiaServerManager) constructStorageVolume(repo *backupv1alpha1.KopiaRepository) corev1.Volume {
	volume := corev1.Volume{Name: "repository"}

	if repo.Spec.FileSystemOptions.NFSServer != "" {
		volume.VolumeSource = corev1.VolumeSource{
			NFS: &corev1.NFSVolumeSource{
				Server: repo.Spec.FileSystemOptions.NFSServer,
				Path:   repo.Spec.FileSystemOptions.NFSPath,
			},
		}
	} else {
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

// quantityToMB converts a resource.Quantity to megabytes.
func quantityToMB(q resource.Quantity) int64 {
	return q.Value() / (1024 * 1024)
}

// BuildCacheFlags generates Kopia cache configuration flags from repository spec.
func BuildCacheFlags(caching backupv1alpha1.KopiaRepositoryCachingSpec) string {
	var b strings.Builder
	if caching.CacheDirectory != "" {
		fmt.Fprintf(&b, " --cache-directory=%s", caching.CacheDirectory)
	}
	if !caching.ContentCacheSize.IsZero() {
		fmt.Fprintf(&b, " --content-cache-size-mb=%d", quantityToMB(caching.ContentCacheSize))
	}
	if !caching.ContentCacheSizeLimit.IsZero() {
		fmt.Fprintf(&b, " --content-cache-size-limit-mb=%d", quantityToMB(caching.ContentCacheSizeLimit))
	}
	if !caching.MetadataCacheSize.IsZero() {
		fmt.Fprintf(&b, " --metadata-cache-size-mb=%d", quantityToMB(caching.MetadataCacheSize))
	}
	if !caching.MetadataCacheSizeLimit.IsZero() {
		fmt.Fprintf(&b, " --metadata-cache-size-limit-mb=%d", quantityToMB(caching.MetadataCacheSizeLimit))
	}
	if caching.MaxListCacheDuration > 0 {
		fmt.Fprintf(&b, " --max-list-cache-duration=%ds", caching.MaxListCacheDuration)
	}
	if caching.MinMetadataSweepAge > 0 {
		fmt.Fprintf(&b, " --min-metadata-sweep-age=%ds", caching.MinMetadataSweepAge)
	}
	if caching.MinContentSweepAge > 0 {
		fmt.Fprintf(&b, " --min-content-sweep-age=%ds", caching.MinContentSweepAge)
	}
	if caching.MinIndexSweepAge > 0 {
		fmt.Fprintf(&b, " --min-index-sweep-age=%ds", caching.MinIndexSweepAge)
	}
	return b.String()
}

// constructServerCommand builds the command to start the Kopia Server.
func (m *KopiaServerManager) constructServerCommand(repo *backupv1alpha1.KopiaRepository) string {
	var b strings.Builder

	switch repo.Spec.StorageType {
	case backupv1alpha1.StorageTypeFilesystem:
		m.writeFilesystemServerScript(&b, repo)
	case backupv1alpha1.StorageTypeSFTP:
		m.writeSFTPServerScript(&b, repo)
	default:
		fmt.Fprintf(&b, "set -e\necho \"Connecting to repository...\"\n")
		fmt.Fprintf(&b, "echo 'Unsupported storage type: %s' && exit 1\n", repo.Spec.StorageType)
	}

	b.WriteString("\necho \"Setting up admin user...\"\n")
	m.writeAdminUserSetup(&b, repo)
	b.WriteString("\necho \"Starting Kopia Server...\"\n")
	m.writeServerStartCommand(&b, repo)

	return b.String()
}

// writeFilesystemServerScript writes the filesystem-mode repository connect/create script.
func (m *KopiaServerManager) writeFilesystemServerScript(b *strings.Builder, repo *backupv1alpha1.KopiaRepository) {
	cacheFlags := BuildCacheFlags(repo.Spec.Caching)

	b.WriteString("set -e\necho \"Connecting to repository...\"\n")
	fmt.Fprintf(b, "kopia repository connect filesystem --path=/repository --override-hostname=%s --override-username=%s%s || {\n",
		shellQuote(repo.Spec.Hostname), shellQuote(repo.Spec.Username), cacheFlags)
	b.WriteString("  echo \"Repository connection failed, attempting to create...\"\n")
	fmt.Fprintf(b, "  kopia repository create filesystem --path=/repository --override-hostname=%s --override-username=%s%s\n",
		shellQuote(repo.Spec.Hostname), shellQuote(repo.Spec.Username), cacheFlags)
	b.WriteString("}\n")
}

// writeSFTPServerScript writes the SFTP-mode repository connect/create script.
func (m *KopiaServerManager) writeSFTPServerScript(b *strings.Builder, repo *backupv1alpha1.KopiaRepository) {
	cacheFlags := BuildCacheFlags(repo.Spec.Caching)
	port := int32(22)
	if repo.Spec.SFTPOptions.Port > 0 {
		port = repo.Spec.SFTPOptions.Port
	}

	b.WriteString("echo \"Connecting to repository...\"\n")
	m.writeSFTPCredentialSetup(b, repo)

	// Build the connect command
	baseSFTPCmd := fmt.Sprintf("kopia repository connect sftp --host=%s --port=%d --path=%s",
		shellQuote(repo.Spec.SFTPOptions.Host), port, shellQuote(repo.Spec.SFTPOptions.Path))
	fmt.Fprintf(b, "SFTP_CMD=\"%s --username=$SFTP_USER\"\n", baseSFTPCmd)
	m.writeSFTPAuthFlags(b, repo, "SFTP_CMD")
	fmt.Fprintf(b, "SFTP_CMD=\"$SFTP_CMD --override-hostname=%s --override-username=%s%s\"\n",
		shellQuote(repo.Spec.Hostname), shellQuote(repo.Spec.Username), cacheFlags)
	b.WriteString("eval \"$SFTP_CMD\"\n")

	// Build the create fallback
	b.WriteString("if [ $? -ne 0 ]; then\n")
	b.WriteString("  echo \"Repository connection failed, attempting to create...\"\n")
	sftpCreateCmd := fmt.Sprintf("kopia repository create sftp --host=%s --port=%d --path=%s",
		shellQuote(repo.Spec.SFTPOptions.Host), port, shellQuote(repo.Spec.SFTPOptions.Path))
	fmt.Fprintf(b, "  SFTP_CREATE_CMD=\"%s --username=$SFTP_USER\"\n", sftpCreateCmd)
	m.writeSFTPAuthFlags(b, repo, "SFTP_CREATE_CMD")
	fmt.Fprintf(b, "  SFTP_CREATE_CMD=\"$SFTP_CREATE_CMD --override-hostname=%s --override-username=%s%s\"\n",
		shellQuote(repo.Spec.Hostname), shellQuote(repo.Spec.Username), cacheFlags)
	b.WriteString("  eval \"$SFTP_CREATE_CMD\"\n")
	b.WriteString("  if [ $? -eq 0 ]; then\n")
	b.WriteString("    echo \"Repository created successfully, reconnecting...\"\n")
	b.WriteString("    eval \"$SFTP_CMD\"\n")
	b.WriteString("  else\n")
	b.WriteString("    echo \"Failed to create repository\"; exit 1\n")
	b.WriteString("  fi\n")
	b.WriteString("fi\n")
	b.WriteString("set -e\n")
}

// writeSFTPCredentialSetup writes the SFTP credential loading script fragment.
func (m *KopiaServerManager) writeSFTPCredentialSetup(b *strings.Builder, _ *backupv1alpha1.KopiaRepository) {
	b.WriteString(`SFTP_USER=$(cat /sftp-creds/username 2>/dev/null || echo "")
SFTP_PASSWORD=$(cat /sftp-creds/password 2>/dev/null || echo "")
SFTP_KEY=$(cat /sftp-creds/keyData 2>/dev/null || echo "")
SFTP_KNOWN_HOSTS=$(cat /sftp-creds/knownHostsData 2>/dev/null || echo "")
if [ -z "$SFTP_USER" ]; then echo "ERROR: SFTP username not found in secret"; exit 1; fi
`)
}

// writeSFTPAuthFlags appends SFTP authentication flags to the given command variable.
func (m *KopiaServerManager) writeSFTPAuthFlags(b *strings.Builder, repo *backupv1alpha1.KopiaRepository, cmdVar string) {
	// Register cleanup trap for temp files before creating them.
	b.WriteString("CLEANUP_FILES=\"\"\n")
	b.WriteString("trap 'rm -f $CLEANUP_FILES' EXIT\n")

	fmt.Fprintf(b, `if [ -n "$SFTP_KEY" ]; then
  SSH_KEY_FILE=$(mktemp /tmp/ssh_key.XXXXXX) && echo "$SFTP_KEY" > "$SSH_KEY_FILE" && chmod 600 "$SSH_KEY_FILE"
  CLEANUP_FILES="$CLEANUP_FILES $SSH_KEY_FILE"
  %s="$%s --keyfile=$SSH_KEY_FILE"
elif [ -n "$SFTP_PASSWORD" ]; then
  %s="$%s --sftp-password=$SFTP_PASSWORD"
else
  echo "ERROR: Neither keyData nor password found in secret"; exit 1
fi
`, cmdVar, cmdVar, cmdVar, cmdVar)

	if repo.Spec.SFTPOptions.KnownHostsData != "" {
		// Known hosts data provided inline in the CR spec.
		fmt.Fprintf(b, "cat > /tmp/known_hosts <<'KNOWN_HOSTS_EOF'\n%s\nKNOWN_HOSTS_EOF\n", repo.Spec.SFTPOptions.KnownHostsData)
		b.WriteString("CLEANUP_FILES=\"$CLEANUP_FILES /tmp/known_hosts\"\n")
		fmt.Fprintf(b, "%s=\"$%s --known-hosts=/tmp/known_hosts\"\n", cmdVar, cmdVar)
	} else {
		// Fallback: read knownHostsData from the credentials secret.
		// The secret value may contain literal \n sequences instead of real newlines,
		// so we use printf %b to interpret escape sequences.
		fmt.Fprintf(b, `if [ -n "$SFTP_KNOWN_HOSTS" ]; then
  printf '%%b' "$SFTP_KNOWN_HOSTS" > /tmp/known_hosts
  CLEANUP_FILES="$CLEANUP_FILES /tmp/known_hosts"
  %s="$%s --known-hosts=/tmp/known_hosts"
fi
`, cmdVar, cmdVar)
	}
	if repo.Spec.SFTPOptions.ExternalSSH {
		sshCmd := "ssh"
		if repo.Spec.SFTPOptions.SSHCommand != "" {
			sshCmd = repo.Spec.SFTPOptions.SSHCommand
		}
		fmt.Fprintf(b, "%s=\"$%s --external-ssh --ssh-command=%s\"\n", cmdVar, cmdVar, shellQuote(sshCmd))
	}
}

// writeAdminUserSetup writes the admin user creation/update script fragment.
func (m *KopiaServerManager) writeAdminUserSetup(b *strings.Builder, repo *backupv1alpha1.KopiaRepository) {
	adminUser := shellQuote(fmt.Sprintf("%s@%s", repo.Spec.Username, repo.Spec.Hostname))
	fmt.Fprintf(b, `ADMIN_USER=%s
echo "Checking admin user: $ADMIN_USER"
set +e
kopia server user list 2>/dev/null | grep -q "$ADMIN_USER"
USER_EXISTS=$?
set -e
if [ $USER_EXISTS -ne 0 ]; then
  echo "Creating admin user: $ADMIN_USER"
  kopia server user add "$ADMIN_USER" --user-password="${KOPIA_PASSWORD}"
else
  echo "Updating admin user password: $ADMIN_USER"
  kopia server user set "$ADMIN_USER" --user-password="${KOPIA_PASSWORD}"
fi
`, adminUser)
}

// writeServerStartCommand writes the kopia server start command.
func (m *KopiaServerManager) writeServerStartCommand(b *strings.Builder, repo *backupv1alpha1.KopiaRepository) {
	b.WriteString("kopia server start \\\n")
	b.WriteString("  --tls-cert-file=/tls/tls.crt \\\n")
	b.WriteString("  --tls-key-file=/tls/tls.key \\\n")
	fmt.Fprintf(b, "  --address=0.0.0.0:%d \\\n", defaultServerPort)
	b.WriteString("  --server-control-username=admin \\\n")
	b.WriteString("  --server-control-password=\"${KOPIA_SERVER_PASSWORD}\"")
	for _, arg := range repo.Spec.Server.ExtraArgs {
		fmt.Fprintf(b, " \\\n  %s", shellQuote(arg))
	}
	b.WriteString("\n")
}

// getRepositoryPasswordSecretKeyRef returns the secret key reference for the repository password.
func (m *KopiaServerManager) getRepositoryPasswordSecretKeyRef(repo *backupv1alpha1.KopiaRepository) *corev1.SecretKeySelector {
	return &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{
			Name: repo.Spec.PasswordSecretName,
		},
		Key: "KOPIA_PASSWORD",
	}
}

// getServerAdminPasswordSecretKeyRef returns the secret key reference for the server admin password.
func (m *KopiaServerManager) getServerAdminPasswordSecretKeyRef(repo *backupv1alpha1.KopiaRepository) *corev1.SecretKeySelector {
	if repo.Spec.Server.AdminPasswordSecretName != "" {
		return &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: repo.Spec.Server.AdminPasswordSecretName,
			},
			Key: "password",
		}
	}
	// Fall back to repository password.
	return m.getRepositoryPasswordSecretKeyRef(repo)
}

// getTLSSecretName returns the name of the TLS secret for the server.
func (m *KopiaServerManager) getTLSSecretName(repo *backupv1alpha1.KopiaRepository) string {
	if repo.Spec.Server.TLS.SecretName != "" {
		return repo.Spec.Server.TLS.SecretName
	}
	return naming.TLSSecretName(repo.Name)
}

// EnsureTLSSecret ensures TLS certificates exist for the Kopia Server.
// Returns the SHA256 fingerprint of the certificate.
func (m *KopiaServerManager) EnsureTLSSecret(
	ctx context.Context,
	repo *backupv1alpha1.KopiaRepository,
) (string, error) {
	logger := log.FromContext(ctx)
	tlsSecretName := m.getTLSSecretName(repo)

	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, types.NamespacedName{Name: tlsSecretName, Namespace: repo.Namespace}, secret)

	if err == nil {
		if repo.Spec.Server.TLS.SecretName != "" {
			certPEM, ok := secret.Data["tls.crt"]
			if !ok {
				return "", fmt.Errorf("tls secret %s missing 'tls.crt' key", tlsSecretName)
			}
			if _, ok := secret.Data["tls.key"]; !ok {
				return "", fmt.Errorf("tls secret %s missing 'tls.key' key", tlsSecretName)
			}
			fingerprint, err := calculateCertFingerprint(certPEM)
			if err != nil {
				return "", fmt.Errorf("failed to calculate fingerprint from user-provided cert: %w", err)
			}
			logger.Info("Using user-provided TLS secret", "name", tlsSecretName, "fingerprint", fingerprint)
			return fingerprint, nil
		}

		// Check if auto-generated cert needs rotation (expires within 30 days)
		if certPEM, ok := secret.Data["tls.crt"]; ok {
			if needsRotation, reason := certNeedsRotation(certPEM, 30*24*time.Hour); needsRotation {
				logger.Info("Rotating TLS certificate", "name", tlsSecretName, "reason", reason)
				if err := m.Client.Delete(ctx, secret); err != nil {
					return "", fmt.Errorf("failed to delete expiring TLS secret: %w", err)
				}
				// Fall through to regeneration below
			} else {
				if fingerprint, ok := secret.Data["fingerprint"]; ok {
					return string(fingerprint), nil
				}
				fingerprint, err := calculateCertFingerprint(certPEM)
				if err != nil {
					return "", fmt.Errorf("failed to calculate fingerprint: %w", err)
				}
				return fingerprint, nil
			}
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("failed to get TLS secret %s: %w", tlsSecretName, err)
	}

	if repo.Spec.Server.TLS.SecretName != "" {
		return "", fmt.Errorf("tls secret %s not found: create it with 'tls.crt' and 'tls.key' keys", tlsSecretName)
	}

	logger.Info("Auto-generating TLS certificate", "name", tlsSecretName)

	serviceName := naming.ServerServiceName(repo.Name)

	dnsNames := []string{
		serviceName,
		fmt.Sprintf("%s.%s", serviceName, repo.Namespace),
		fmt.Sprintf("%s.%s.svc", serviceName, repo.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, repo.Namespace),
	}
	dnsNames = append(dnsNames, repo.Spec.Server.TLS.CertificateDNSNames...)

	commonName := repo.Spec.Server.TLS.CertificateCommonName
	if commonName == "" {
		commonName = fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, repo.Namespace)
	}

	certPEM, keyPEM, err := generateSelfSignedCert(commonName, dnsNames)
	if err != nil {
		return "", fmt.Errorf("failed to generate TLS certificate: %w", err)
	}

	fingerprint, err := calculateCertFingerprint(certPEM)
	if err != nil {
		return "", fmt.Errorf("failed to calculate fingerprint: %w", err)
	}

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
		return "", fmt.Errorf("failed to set controller reference on TLS secret: %w", err)
	}

	logger.Info("Creating TLS Secret", "name", tlsSecretName, "fingerprint", fingerprint)
	if err := m.Client.Create(ctx, tlsSecret); err != nil {
		return "", fmt.Errorf("failed to create TLS secret %s: %w", tlsSecretName, err)
	}

	return fingerprint, nil
}

// calculateCertFingerprint calculates the SHA256 fingerprint of a PEM-encoded certificate.
func calculateCertFingerprint(certPEM []byte) (string, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %w", err)
	}
	hash := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("%X", hash), nil
}

// certNeedsRotation checks if a PEM-encoded certificate expires within the given threshold.
func certNeedsRotation(certPEM []byte, renewBefore time.Duration) (bool, string) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return true, "invalid PEM data"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true, fmt.Sprintf("failed to parse certificate: %v", err)
	}
	remaining := time.Until(cert.NotAfter)
	if remaining <= 0 {
		return true, "certificate has expired"
	}
	if remaining < renewBefore {
		return true, fmt.Sprintf("certificate expires in %s (threshold: %s)", remaining.Round(time.Hour), renewBefore)
	}
	return false, ""
}

// generateSelfSignedCert generates a self-signed TLS certificate.
func generateSelfSignedCert(commonName string, dnsNames []string) ([]byte, []byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

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
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	return certPEM, keyPEM, nil
}
