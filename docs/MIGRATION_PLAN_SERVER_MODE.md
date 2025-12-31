# Migration Plan: Direct Storage → Kopia Server Architecture

## Executive Summary

This document outlines the plan to migrate the kopia-operator from a **direct storage access** model to a **centralized Kopia Server** model. This architectural change will:

1. Deploy a Kopia Server per KopiaRepository
2. Manage user credentials for each KopiaBackup
3. Expose the server via Ingress/HTTPRoute
4. Eliminate direct storage credentials from backup pods
5. Provide centralized access control and monitoring

## Current Architecture vs. Target Architecture

### Current Architecture (Direct Storage)

```text
┌────────────────────────────────────────────────────────────────┐
│  Backup CronJob Pod                                            │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ - Mounts PVC                                             │  │
│  │ - Has storage credentials (NFS/SFTP)                     │  │
│  │ - Connects directly to backend storage                   │  │
│  │ - Creates snapshots                                      │  │
│  └──────────────────────────────────────────────────────────┘  │
│                           │                                     │
│                           ▼                                     │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │         NFS Server / SFTP Server                         │  │
│  │         (Direct Storage Backend)                         │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────┘
```

**Issues:**

- Every backup pod needs storage credentials
- Direct storage exposure to all backup pods
- No centralized access control
- Difficult to monitor and audit
- Storage credentials scattered across namespaces

### Target Architecture (Kopia Server)

```text
┌────────────────────────────────────────────────────────────────┐
│  Backup CronJob Pod (per PVC)                                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ - Mounts PVC                                             │  │
│  │ - Has only Kopia user credentials                        │  │
│  │ - Connects to Kopia Server via HTTPS                     │  │
│  │ - Creates snapshots via API                              │  │
│  └──────────────────────────────────────────────────────────┘  │
│                           │                                     │
│                           ▼                                     │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │         Kopia Server (per KopiaRepository)               │  │
│  │  ┌────────────────────────────────────────────────────┐  │  │
│  │  │ - Multi-user authentication                        │  │  │
│  │  │ - User management (one user per KopiaBackup)       │  │  │
│  │  │ - Exposed via Ingress/HTTPRoute                    │  │  │
│  │  │ - Centralized monitoring                           │  │  │
│  │  │ - TLS termination                                  │  │  │
│  │  └────────────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────────────┘  │
│                           │                                     │
│                           ▼                                     │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │         NFS Server / SFTP Server                         │  │
│  │         (Only accessible by Kopia Server)                │  │
│  └──────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────┘
```

**Benefits:**

- Centralized credential management
- Storage credentials only in Kopia Server
- Per-backup user authentication
- Centralized monitoring and logging
- Better security posture
- Easier to implement network policies

## Implementation Plan

### Phase 1: API Changes (CRD Updates)

#### 1.1 Update KopiaRepository CRD

Add new fields to support Kopia Server deployment:

```go
type KopiaRepositorySpec struct {
    // Existing fields...
    Hostname              string
    Username              string
    StorageType           string
    // ... existing fields ...

    // NEW: Server configuration
    Server KopiaServerSpec `json:"server,omitempty"`
}

type KopiaServerSpec struct {
    // Enable Kopia Server mode (default: true for new installations)
    Enabled bool `json:"enabled"`

    // Server deployment configuration
    Image           string `json:"image,omitempty"`          // Default: ghcr.io/fastlorenzo/kopia:latest
    Replicas        int32  `json:"replicas,omitempty"`       // Default: 1
    Resources       corev1.ResourceRequirements `json:"resources,omitempty"`

    // Server TLS configuration
    TLS KopiaServerTLSSpec `json:"tls,omitempty"`

    // Server exposure configuration
    Exposure KopiaServerExposureSpec `json:"exposure"`

    // Server admin credentials (for operator to manage users)
    AdminPasswordExistingSecret string `json:"adminPasswordExistingSecret,omitempty"`

    // Server storage for internal state
    PersistentVolumeClaim string `json:"persistentVolumeClaim,omitempty"`

    // Additional server flags/configuration
    ExtraArgs []string `json:"extraArgs,omitempty"`
}

type KopiaServerTLSSpec struct {
    // Enable TLS
    Enabled bool `json:"enabled"`

    // Certificate configuration
    SecretName string `json:"secretName,omitempty"`

    // Auto-generate self-signed cert if secret not provided
    AutoGenerate bool `json:"autoGenerate,omitempty"`
}

type KopiaServerExposureSpec struct {
    // Type of exposure: Service, Ingress, HTTPRoute
    Type string `json:"type"` // "Service", "Ingress", "HTTPRoute"

    // Service configuration
    ServiceType corev1.ServiceType `json:"serviceType,omitempty"` // ClusterIP, LoadBalancer, NodePort
    ServicePort int32              `json:"servicePort,omitempty"` // Default: 51515

    // Ingress configuration
    IngressClassName string            `json:"ingressClassName,omitempty"`
    Host            string             `json:"host,omitempty"`
    Annotations     map[string]string  `json:"annotations,omitempty"`

    // HTTPRoute configuration (Gateway API)
    GatewayName      string `json:"gatewayName,omitempty"`
    GatewayNamespace string `json:"gatewayNamespace,omitempty"`
}
```

#### 1.2 Update KopiaBackup CRD

Add fields for Kopia Server authentication:

```go
type KopiaBackupSpec struct {
    // Existing fields...
    PVCName    string
    Schedule   string
    Repository string
    Suspend    bool

    // NEW: Server user credentials
    // Username will be auto-generated: <namespace>-<pvcname>
    // Password will be auto-generated and stored in a secret
    UserCredentialsSecret string `json:"userCredentialsSecret,omitempty"` // Read-only, set by operator
}

type KopiaBackupStatus struct {
    // Existing fields...
    Active         bool
    FromAnnotation bool

    // NEW: Server connection status
    ServerURL      string `json:"serverURL,omitempty"`
    Username       string `json:"username,omitempty"`
    Connected      bool   `json:"connected,omitempty"`
    LastBackupTime *metav1.Time `json:"lastBackupTime,omitempty"`
}
```

### Phase 2: Core Components

#### 2.1 New Component: Kopia Server Manager

Create a new file: `internal/controller/backup/kopiaserver_manager.go`

**Responsibilities:**

- Deploy/Update Kopia Server Deployment
- Create/Update Service
- Create/Update Ingress/HTTPRoute
- Manage TLS certificates
- Initialize server repository
- Manage server lifecycle

**Key Functions:**

```go
type KopiaServerManager struct {
    Client client.Client
    Scheme *runtime.Scheme
    Log    logr.Logger
}

// EnsureServerDeployment creates or updates the Kopia Server deployment
func (m *KopiaServerManager) EnsureServerDeployment(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
) error

// EnsureServerService creates or updates the Service
func (m *KopiaServerManager) EnsureServerService(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
) (*corev1.Service, error)

// EnsureServerExposure creates Ingress or HTTPRoute
func (m *KopiaServerManager) EnsureServerExposure(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
    svc *corev1.Service,
) error

// InitializeServer initializes the Kopia repository on the server
func (m *KopiaServerManager) InitializeServer(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
) error

// GetServerURL returns the URL to connect to the server
func (m *KopiaServerManager) GetServerURL(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
) (string, error)
```

#### 2.2 New Component: Kopia User Manager

Create a new file: `internal/controller/backup/kopiauser_manager.go`

**Responsibilities:**

- Create users on Kopia Server via API
- Generate secure passwords
- Store credentials in Secrets
- Delete users when backup is removed
- Update user policies

**Key Functions:**

```go
type KopiaUserManager struct {
    Client     client.Client
    Scheme     *runtime.Scheme
    Log        logr.Logger
    HTTPClient *http.Client
}

// EnsureUser creates or updates a user on the Kopia Server
func (m *KopiaUserManager) EnsureUser(
    ctx context.Context,
    backup *backupv1alpha1.KopiaBackup,
    repo *backupv1alpha1.KopiaRepository,
    serverURL string,
) (*corev1.Secret, error)

// DeleteUser removes a user from the Kopia Server
func (m *KopiaUserManager) DeleteUser(
    ctx context.Context,
    backup *backupv1alpha1.KopiaBackup,
    repo *backupv1alpha1.KopiaRepository,
    serverURL string,
) error

// GenerateUsername creates a unique username for the backup
func (m *KopiaUserManager) GenerateUsername(
    backup *backupv1alpha1.KopiaBackup,
) string // Format: <namespace>-<pvcname>

// GenerateSecurePassword generates a random password
func (m *KopiaUserManager) GenerateSecurePassword() string

// GetServerAdminCredentials retrieves admin credentials from secret
func (m *KopiaUserManager) GetServerAdminCredentials(
    ctx context.Context,
    repo *backupv1alpha1.KopiaRepository,
) (username, password string, error)

// CreateUserViaAPI calls Kopia Server API to create user
func (m *KopiaUserManager) CreateUserViaAPI(
    serverURL, adminUser, adminPass, username, password string,
) error
```

#### 2.3 Update KopiaRepositoryReconciler

Modify: `internal/controller/backup/kopiarepository_controller.go`

**New Responsibilities:**

- Deploy Kopia Server when `server.enabled = true`
- Manage server lifecycle
- Update repository status with server URL
- Handle server upgrades

**Updated Reconcile Logic:**

```go
func (r *KopiaRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch KopiaRepository
    // 2. Validate configuration

    // NEW: 3. Check if server mode is enabled
    if repo.Spec.Server.Enabled {
        serverManager := NewKopiaServerManager(r.Client, r.Scheme, r.Log)

        // 3a. Ensure admin secret exists
        if err := r.ensureAdminSecret(ctx, &repo); err != nil {
            return ctrl.Result{}, err
        }

        // 3b. Deploy/Update Kopia Server
        if err := serverManager.EnsureServerDeployment(ctx, &repo); err != nil {
            return ctrl.Result{}, err
        }

        // 3c. Ensure Service
        svc, err := serverManager.EnsureServerService(ctx, &repo)
        if err != nil {
            return ctrl.Result{}, err
        }

        // 3d. Ensure Exposure (Ingress/HTTPRoute)
        if err := serverManager.EnsureServerExposure(ctx, &repo, svc); err != nil {
            return ctrl.Result{}, err
        }

        // 3e. Wait for server to be ready
        if !r.isServerReady(ctx, &repo) {
            return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
        }

        // 3f. Initialize repository on server (if not initialized)
        if err := serverManager.InitializeServer(ctx, &repo); err != nil {
            return ctrl.Result{}, err
        }

        // 3g. Update status with server URL
        serverURL, _ := serverManager.GetServerURL(ctx, &repo)
        repo.Status.ServerURL = serverURL
        repo.Status.ServerReady = true
    }

    // 4. Update status
    return ctrl.Result{}, r.Status().Update(ctx, &repo)
}
```

#### 2.4 Update KopiaBackupReconciler

Modify: `internal/controller/backup/kopiabackup_controller.go`

**New Responsibilities:**

- Create user credentials for backup
- Configure CronJob to connect to Kopia Server instead of direct storage
- Remove direct storage configuration

**Updated Reconcile Logic:**

```go
func (r *KopiaBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1-5. Existing logic (fetch backup, verify PVC, get repository, etc.)

    // 6. Get repository
    repository, err := getKopiaRepositoryByName(ctx, r.Client, kBackup.Spec.Repository, log)

    // NEW: 7. Handle server mode
    if repository.Spec.Server.Enabled {
        // 7a. Wait for server to be ready
        if !repository.Status.ServerReady {
            log.Info("Waiting for Kopia Server to be ready")
            return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
        }

        // 7b. Ensure user credentials
        userManager := NewKopiaUserManager(r.Client, r.Scheme, r.Log)
        secret, err := userManager.EnsureUser(ctx, &kBackup, repository, repository.Status.ServerURL)
        if err != nil {
            return ctrl.Result{}, err
        }

        // 7c. Update backup status
        kBackup.Status.ServerURL = repository.Status.ServerURL
        kBackup.Status.Username = userManager.GenerateUsername(&kBackup)
        kBackup.Spec.UserCredentialsSecret = secret.Name
    }

    // 8. Get runtime info (node, app, pod)
    // 9. Create/Update CronJob
    //    - CronJob will use different configuration for server mode
    //    - No ConfigMap needed for server mode
    //    - No direct storage mounts needed

    return ctrl.Result{}, nil
}
```

#### 2.5 Update CronJob Construction

Modify: `constructCronJob()` function in `kopiabackup_controller.go`

**Changes:**

```go
func constructCronJob(
    backup *backupv1alpha1.KopiaBackup,
    cronJobName string,
    nodeName string,
    appName string,
    repo *backupv1alpha1.KopiaRepository,
) *batchv1.CronJob {

    var envVars []corev1.EnvVar
    var volumeMounts []corev1.VolumeMount
    var volumes []corev1.Volume
    var command []string

    // Always mount the PVC being backed up
    mountPath := "/data/" + backup.Namespace + "/" + backup.Spec.PVCName
    volumes = append(volumes, corev1.Volume{
        Name: "data",
        VolumeSource: corev1.VolumeSource{
            PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
                ClaimName: backup.Spec.PVCName,
            },
        },
    })
    volumeMounts = append(volumeMounts, corev1.VolumeMount{
        Name:      "data",
        MountPath: mountPath,
    })

    // NEW: Server mode vs Direct mode
    if repo.Spec.Server.Enabled {
        // SERVER MODE: Connect to Kopia Server

        // Mount user credentials
        volumes = append(volumes, corev1.Volume{
            Name: "kopia-credentials",
            VolumeSource: corev1.VolumeSource{
                Secret: &corev1.SecretVolumeSource{
                    SecretName: backup.Spec.UserCredentialsSecret,
                },
            },
        })

        // Set environment variables for server connection
        envVars = append(envVars,
            corev1.EnvVar{Name: "KOPIA_SERVER_URL", Value: backup.Status.ServerURL},
            corev1.EnvVar{Name: "KOPIA_SERVER_USERNAME", Value: backup.Status.Username},
            corev1.EnvVar{
                Name: "KOPIA_SERVER_PASSWORD",
                ValueFrom: &corev1.EnvVarSource{
                    SecretKeyRef: &corev1.SecretKeySelector{
                        LocalObjectReference: corev1.LocalObjectReference{
                            Name: backup.Spec.UserCredentialsSecret,
                        },
                        Key: "password",
                    },
                },
            },
        )

        // Kopia command for server mode
        command = []string{"/bin/bash", "-c", strings.Join([]string{
            "printf \"[01/04] Connect to server...\\n\" && " +
            "kopia server login " +
            "--url=$KOPIA_SERVER_URL " +
            "--username=$KOPIA_SERVER_USERNAME " +
            "--password=$KOPIA_SERVER_PASSWORD",

            "printf \"[02/04] Create snapshot...\\n\" && " +
            "kopia snapshot create " + mountPath,

            "printf \"[03/04] List snapshots...\\n\" && " +
            "kopia snapshot list " + mountPath,

            "printf \"[04/04] Disconnect...\\n\" && " +
            "kopia server logout",
        }, " && ")}

    } else {
        // DIRECT MODE: Existing logic for direct storage access
        // ... existing implementation ...
    }

    // Build CronJob spec
    cronJob := &batchv1.CronJob{
        // ... rest of CronJob configuration
        Spec: batchv1.CronJobSpec{
            JobTemplate: batchv1.JobTemplateSpec{
                Spec: batchv1.JobSpec{
                    Template: corev1.PodTemplateSpec{
                        Spec: corev1.PodSpec{
                            Containers: []corev1.Container{
                                {
                                    Name:         "snapshot",
                                    Image:        "ghcr.io/fastlorenzo/kopia:0.20.1",
                                    Command:      []string{"/bin/bash", "-c"},
                                    Args:         command,
                                    Env:          envVars,
                                    VolumeMounts: volumeMounts,
                                },
                            },
                            Volumes: volumes,
                            // ... rest of pod spec
                        },
                    },
                },
            },
        },
    }

    return cronJob
}
```

### Phase 3: RBAC and Permissions

#### 3.1 Update ClusterRole

Add permissions for new resources:

```yaml
# config/rbac/role.yaml

# Existing permissions...

# NEW: Deployment management for Kopia Server
- apiGroups:
    - apps
  resources:
    - deployments
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch

# NEW: Service management
- apiGroups:
    - ""
  resources:
    - services
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch

# NEW: Secret management for user credentials
- apiGroups:
    - ""
  resources:
    - secrets
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch

# NEW: Ingress management
- apiGroups:
    - networking.k8s.io
  resources:
    - ingresses
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch

# NEW: HTTPRoute management (Gateway API)
- apiGroups:
    - gateway.networking.k8s.io
  resources:
    - httproutes
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch

# NEW: PVC management for server storage
- apiGroups:
    - ""
  resources:
    - persistentvolumeclaims
  verbs:
    - create
    - delete
    - get
    - list
    - patch
    - update
    - watch
```

### Phase 4: Kopia Server Deployment Templates

#### 4.1 Server Deployment Template

```go
// internal/controller/backup/kopiaserver_deployment.go

func buildServerDeployment(repo *backupv1alpha1.KopiaRepository) *appsv1.Deployment {
    labels := map[string]string{
        "app.kubernetes.io/name":       "kopia-server",
        "app.kubernetes.io/instance":   repo.Name,
        "app.kubernetes.io/component":  "backup-server",
        "app.kubernetes.io/managed-by": "kopia-operator",
    }

    replicas := repo.Spec.Server.Replicas
    if replicas == 0 {
        replicas = 1
    }

    image := repo.Spec.Server.Image
    if image == "" {
        image = "ghcr.io/fastlorenzo/kopia:0.20.1"
    }

    return &appsv1.Deployment{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("kopia-server-%s", repo.Name),
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
                    Containers: []corev1.Container{
                        {
                            Name:  "kopia-server",
                            Image: image,
                            Command: []string{
                                "/bin/kopia",
                                "server",
                                "start",
                                "--address=0.0.0.0:51515",
                                "--server-control-password=$(KOPIA_SERVER_CONTROL_PASSWORD)",
                                "--tls-cert-file=/tls/tls.crt",
                                "--tls-key-file=/tls/tls.key",
                            },
                            Ports: []corev1.ContainerPort{
                                {
                                    Name:          "https",
                                    ContainerPort: 51515,
                                    Protocol:      corev1.ProtocolTCP,
                                },
                            },
                            Env: buildServerEnvVars(repo),
                            VolumeMounts: buildServerVolumeMounts(repo),
                            Resources: repo.Spec.Server.Resources,
                            LivenessProbe: &corev1.Probe{
                                ProbeHandler: corev1.ProbeHandler{
                                    HTTPGet: &corev1.HTTPGetAction{
                                        Path:   "/api/v1/repo/status",
                                        Port:   intstr.FromInt(51515),
                                        Scheme: corev1.URISchemeHTTPS,
                                    },
                                },
                                InitialDelaySeconds: 30,
                                PeriodSeconds:       10,
                            },
                            ReadinessProbe: &corev1.Probe{
                                ProbeHandler: corev1.ProbeHandler{
                                    HTTPGet: &corev1.HTTPGetAction{
                                        Path:   "/api/v1/repo/status",
                                        Port:   intstr.FromInt(51515),
                                        Scheme: corev1.URISchemeHTTPS,
                                    },
                                },
                                InitialDelaySeconds: 10,
                                PeriodSeconds:       5,
                            },
                        },
                    },
                    Volumes: buildServerVolumes(repo),
                },
            },
        },
    }
}
```

#### 4.2 Server Service Template

```go
func buildServerService(repo *backupv1alpha1.KopiaRepository) *corev1.Service {
    labels := map[string]string{
        "app.kubernetes.io/name":      "kopia-server",
        "app.kubernetes.io/instance":  repo.Name,
        "app.kubernetes.io/component": "backup-server",
    }

    port := repo.Spec.Server.Exposure.ServicePort
    if port == 0 {
        port = 51515
    }

    serviceType := repo.Spec.Server.Exposure.ServiceType
    if serviceType == "" {
        serviceType = corev1.ServiceTypeClusterIP
    }

    return &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name:      fmt.Sprintf("kopia-server-%s", repo.Name),
            Namespace: repo.Namespace,
            Labels:    labels,
        },
        Spec: corev1.ServiceSpec{
            Type:     serviceType,
            Selector: labels,
            Ports: []corev1.ServicePort{
                {
                    Name:       "https",
                    Port:       port,
                    TargetPort: intstr.FromInt(51515),
                    Protocol:   corev1.ProtocolTCP,
                },
            },
        },
    }
}
```

#### 4.3 Ingress Template

```go
func buildIngress(repo *backupv1alpha1.KopiaRepository, svc *corev1.Service) *networkingv1.Ingress {
    labels := map[string]string{
        "app.kubernetes.io/name":      "kopia-server",
        "app.kubernetes.io/instance":  repo.Name,
    }

    pathType := networkingv1.PathTypePrefix

    return &networkingv1.Ingress{
        ObjectMeta: metav1.ObjectMeta{
            Name:        fmt.Sprintf("kopia-server-%s", repo.Name),
            Namespace:   repo.Namespace,
            Labels:      labels,
            Annotations: repo.Spec.Server.Exposure.Annotations,
        },
        Spec: networkingv1.IngressSpec{
            IngressClassName: &repo.Spec.Server.Exposure.IngressClassName,
            TLS: []networkingv1.IngressTLS{
                {
                    Hosts:      []string{repo.Spec.Server.Exposure.Host},
                    SecretName: repo.Spec.Server.TLS.SecretName,
                },
            },
            Rules: []networkingv1.IngressRule{
                {
                    Host: repo.Spec.Server.Exposure.Host,
                    IngressRuleValue: networkingv1.IngressRuleValue{
                        HTTP: &networkingv1.HTTPIngressRuleValue{
                            Paths: []networkingv1.HTTPIngressPath{
                                {
                                    Path:     "/",
                                    PathType: &pathType,
                                    Backend: networkingv1.IngressBackend{
                                        Service: &networkingv1.IngressServiceBackend{
                                            Name: svc.Name,
                                            Port: networkingv1.ServiceBackendPort{
                                                Number: svc.Spec.Ports[0].Port,
                                            },
                                        },
                                    },
                                },
                            },
                        },
                    },
                },
            },
        },
    }
}
```

### Phase 5: Migration Strategy

#### 5.1 Backward Compatibility

To maintain backward compatibility:

```go
// In KopiaRepositorySpec
type KopiaRepositorySpec struct {
    // ... existing fields ...

    // Server configuration (optional, defaults to enabled for new repos)
    Server KopiaServerSpec `json:"server,omitempty"`
}

// Default behavior
func (r *KopiaRepository) SetDefaults() {
    // For new repositories created after migration, enable server mode by default
    if r.CreationTimestamp.IsZero() {
        if r.Spec.Server.Image == "" {
            r.Spec.Server.Enabled = true
        }
    }

    // Existing repositories continue with direct mode unless explicitly migrated
}
```

#### 5.2 Migration Path for Existing Deployments

1. **Update CRDs** - Deploy new CRD versions with server fields
2. **Deploy new operator** - Updated operator with server support
3. **Migrate repositories** - Update KopiaRepository resources to enable server mode
4. **Update backups** - Backups will be reconciled automatically

Example migration:

```bash
# Add server configuration to existing repository
kubectl patch kopiarepository nfs-backup-repo --type='merge' -p '{
  "spec": {
    "server": {
      "enabled": true,
      "exposure": {
        "type": "Ingress",
        "host": "kopia-nfs.example.com",
        "ingressClassName": "nginx"
      }
    }
  }
}'

# Operator will:
# 1. Deploy Kopia Server
# 2. Wait for server readiness
# 3. Migrate existing backups to use server
# 4. Create user credentials for each backup
```

### Phase 6: Testing Strategy

#### 6.1 Unit Tests

Files to create/update:

- `internal/controller/backup/kopiaserver_manager_test.go`
- `internal/controller/backup/kopiauser_manager_test.go`
- Update existing `kopiabackup_controller_test.go`
- Update existing `kopiarepository_controller_test.go`

Test cases:

```go
// Test server deployment creation
func TestEnsureServerDeployment(t *testing.T) {
    // Test cases:
    // - Server deployment is created with correct spec
    // - Server deployment is updated when repo spec changes
    // - Server deployment uses custom image when specified
    // - Server deployment has correct resource limits
}

// Test user creation
func TestEnsureUser(t *testing.T) {
    // Test cases:
    // - User is created on server
    // - User credentials are stored in secret
    // - User is updated when backup changes
    // - User is deleted when backup is deleted
}

// Test exposure creation
func TestEnsureServerExposure(t *testing.T) {
    // Test cases:
    // - Ingress is created correctly
    // - HTTPRoute is created correctly
    // - Service type is respected
}
```

#### 6.2 Integration Tests

Update `test/e2e/e2e_test.go`:

```go
func TestServerModeEndToEnd(t *testing.T) {
    // 1. Create KopiaRepository with server enabled
    // 2. Wait for server deployment to be ready
    // 3. Create KopiaBackup
    // 4. Verify user is created
    // 5. Verify CronJob is created with server credentials
    // 6. Trigger backup job manually
    // 7. Verify snapshot is created on server
    // 8. Delete backup
    // 9. Verify user is deleted
    // 10. Delete repository
    // 11. Verify server deployment is deleted
}
```

### Phase 7: Documentation Updates

#### 7.1 Files to Create/Update

1. **Update ARCHITECTURE.md**

   - Add server mode architecture diagrams
   - Explain dual-mode operation
   - Document server lifecycle

2. **Update docs/EXAMPLES.md**

   - Add server mode examples
   - Show Ingress configurations
   - Show HTTPRoute configurations
   - Migration examples

3. **Create docs/SERVER_MODE.md**

   - Detailed server mode documentation
   - API reference for server configuration
   - Security considerations
   - Troubleshooting guide

4. **Update README.md**
   - Mention server mode as primary deployment method
   - Quick start with server mode

### Phase 8: Implementation Timeline

**Week 1-2: Foundation**

- Update CRDs with new fields
- Create KopiaServerManager skeleton
- Create KopiaUserManager skeleton
- Update RBAC permissions

**Week 3-4: Core Implementation**

- Implement server deployment logic
- Implement service and exposure logic
- Implement user management
- Update KopiaRepositoryReconciler

**Week 5-6: Backup Integration**

- Update KopiaBackupReconciler
- Update CronJob construction
- Implement server readiness checks
- Handle migration scenarios

**Week 7-8: Testing & Polish**

- Write unit tests
- Write integration tests
- Update documentation
- Performance testing
- Security review

**Week 9-10: Migration & Release**

- Beta release for testing
- Migration guide
- Final documentation
- GA release

## Security Considerations

### 1. Credential Management

- Admin credentials stored in Kubernetes Secrets
- User credentials auto-generated and stored in Secrets
- Secrets scoped to namespaces
- Support for external secret managers (future enhancement)

### 2. Network Security

- TLS encryption for all server connections
- Optional mTLS for backup pods
- Network policies to restrict server access
- Ingress annotations for additional security (rate limiting, auth)

### 3. Access Control

- One user per backup (principle of least privilege)
- Users can only access their own snapshots
- Admin API protected with separate credentials
- RBAC for operator actions

### 4. Audit Logging

- Server logs all backup operations
- User creation/deletion logged
- Failed authentication attempts logged
- Integration with monitoring systems

## Performance Considerations

### 1. Server Scaling

- Support for multiple replicas (future)
- Resource limits and requests
- Horizontal Pod Autoscaling (future)

### 2. Connection Pooling

- Reuse server connections when possible
- Connection timeout configuration
- Retry logic for transient failures

### 3. Resource Usage

- Server memory/CPU requirements
- Storage for server state
- Network bandwidth for backups

## Monitoring & Observability

### 1. Metrics

Expose Prometheus metrics:

- `kopia_server_users_total` - Total number of users
- `kopia_server_backup_operations_total` - Total backup operations
- `kopia_server_backup_bytes_total` - Total bytes backed up
- `kopia_server_connection_errors_total` - Connection errors
- `kopia_backup_last_success_timestamp` - Last successful backup time

### 2. Health Checks

- Server liveness probe
- Server readiness probe
- Repository health check endpoint

### 3. Logging

- Structured logging (JSON)
- Log levels (debug, info, warn, error)
- Correlation IDs for tracing

## Open Questions & Decisions Needed

1. **Server High Availability**

   - Single replica initially, or support HA from start?
   - StatefulSet vs Deployment?
   - Shared storage for multiple replicas?

2. **Certificate Management**

   - cert-manager integration?
   - Self-signed certificates acceptable?
   - Custom CA support?

3. **User Management API**

   - Use Kopia's built-in user management?
   - Custom user database?
   - Integration with external auth (LDAP, OAuth)?

4. **Migration**

   - Automatic migration of existing backups?
   - Manual migration required?
   - Support both modes indefinitely?

5. **Namespace Strategy**
   - One server per repository (current plan)?
   - Shared server across repositories?
   - Cross-namespace server access?

## Success Criteria

1. ✅ Kopia Server automatically deployed for each repository
2. ✅ User credentials automatically created and managed
3. ✅ Backup pods connect to server instead of direct storage
4. ✅ Server exposed via Ingress or HTTPRoute
5. ✅ Backward compatibility maintained
6. ✅ Migration path documented
7. ✅ Security improved (no storage credentials in backup pods)
8. ✅ Comprehensive tests passing
9. ✅ Documentation complete
10. ✅ Performance acceptable (no regression)

## Conclusion

This migration plan provides a structured approach to evolve the kopia-operator from direct storage access to a centralized Kopia Server architecture. The implementation is designed to:

- Maintain backward compatibility
- Improve security posture
- Simplify credential management
- Enable better monitoring and control
- Provide a clear migration path

The phased approach allows for incremental development and testing, reducing risk and enabling early feedback.
