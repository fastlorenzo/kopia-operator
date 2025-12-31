# Kopia Operator: Server Mode Migration - Quick Start

## Overview

This document provides a quick summary of the migration plan to move from direct storage access to a centralized Kopia Server architecture.

See [MIGRATION_PLAN_SERVER_MODE.md](./MIGRATION_PLAN_SERVER_MODE.md) for the complete detailed plan.

## High-Level Changes

### Architecture Shift

**Before (Direct Storage):**

```
Backup Pod → Direct Access → NFS/SFTP Storage
```

**After (Server Mode):**

```
Backup Pod → Kopia Server (per repo) → NFS/SFTP Storage
```

## Key Benefits

1. **Security**: Storage credentials only in Kopia Server, not in every backup pod
2. **Access Control**: One user per backup with isolated permissions
3. **Centralized Management**: Single point for monitoring, logging, and policy enforcement
4. **Simplified Networking**: No direct storage exposure to all pods
5. **Better Auditing**: All backup operations logged centrally

## New Components

### 1. Kopia Server Manager (`kopiaserver_manager.go`)

Manages the Kopia Server lifecycle:

- Deploys Kopia Server Deployment
- Creates Service
- Creates Ingress/HTTPRoute for external access
- Initializes repository on server
- Manages TLS certificates

### 2. Kopia User Manager (`kopiauser_manager.go`)

Manages per-backup user credentials:

- Creates unique user for each KopiaBackup
- Generates secure passwords
- Stores credentials in Secrets
- Calls Kopia Server API for user management
- Deletes users when backups are removed

## API Changes Summary

### KopiaRepository CRD - New Fields

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: example-repo
spec:
  # Existing fields remain...
  hostname: k8s-cluster
  username: backup-user
  storageType: filesystem

  # NEW: Server configuration
  server:
    enabled: true # Enable server mode
    image: ghcr.io/fastlorenzo/kopia:0.20.1
    replicas: 1

    # TLS configuration
    tls:
      enabled: true
      autoGenerate: true # Auto-generate self-signed cert
      secretName: kopia-tls # Or use existing cert

    # Exposure configuration
    exposure:
      type: Ingress # Ingress, HTTPRoute, or Service
      host: kopia.example.com
      ingressClassName: nginx
      serviceType: ClusterIP
      servicePort: 51515
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod

    # Admin credentials for operator
    adminPasswordExistingSecret: kopia-admin-secret

    # Resources
    resources:
      requests:
        memory: "512Mi"
        cpu: "500m"
      limits:
        memory: "2Gi"
        cpu: "2000m"
```

### KopiaBackup CRD - New Fields

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: my-backup
spec:
  # Existing fields remain...
  pvcName: my-data
  repository: example-repo
  schedule: "0 2 * * *"

  # NEW: Automatically populated by operator
  userCredentialsSecret: my-backup-kopia-creds

status:
  # NEW: Server connection info
  serverURL: https://kopia.example.com
  username: default-my-data
  connected: true
  lastBackupTime: "2025-12-31T02:00:00Z"
```

## Implementation Phases

### Phase 1: Foundation (Week 1-2)

- [ ] Update CRD definitions
- [ ] Add RBAC permissions (Deployments, Services, Ingresses, Secrets)
- [ ] Create skeleton for new managers

### Phase 2: Core (Week 3-4)

- [ ] Implement KopiaServerManager
- [ ] Implement server deployment, service, and exposure
- [ ] Update KopiaRepositoryReconciler

### Phase 3: Integration (Week 5-6)

- [ ] Implement KopiaUserManager
- [ ] Update KopiaBackupReconciler
- [ ] Modify CronJob construction for server mode

### Phase 4: Testing (Week 7-8)

- [ ] Unit tests
- [ ] Integration tests
- [ ] Documentation

### Phase 5: Release (Week 9-10)

- [ ] Beta release
- [ ] Migration guide
- [ ] GA release

## Quick Implementation Checklist

### Step 1: Update CRDs

```bash
# Update api/backup/v1alpha1/kopiarepository_types.go
# Add Server, TLS, and Exposure types

# Update api/backup/v1alpha1/kopiabackup_types.go
# Add UserCredentialsSecret and server status fields

# Regenerate
make manifests
make generate
```

### Step 2: Create New Files

```bash
# Create new manager files
touch internal/controller/backup/kopiaserver_manager.go
touch internal/controller/backup/kopiauser_manager.go
touch internal/controller/backup/kopiaserver_deployment.go

# Create test files
touch internal/controller/backup/kopiaserver_manager_test.go
touch internal/controller/backup/kopiauser_manager_test.go
```

### Step 3: Update RBAC

```bash
# Edit config/rbac/role.yaml
# Add permissions for: apps/deployments, core/services, core/secrets,
# networking.k8s.io/ingresses, gateway.networking.k8s.io/httproutes

# Regenerate
make manifests
```

### Step 4: Implement Core Logic

Priority order:

1. Server deployment builder
2. KopiaServerManager.EnsureServerDeployment()
3. KopiaServerManager.EnsureServerService()
4. KopiaServerManager.EnsureServerExposure()
5. KopiaUserManager.EnsureUser()
6. Update KopiaRepositoryReconciler
7. Update KopiaBackupReconciler
8. Update constructCronJob()

## Example Usage (After Implementation)

### Create a Repository with Server

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kopia-admin-secret
  namespace: backup-system
type: Opaque
stringData:
  username: admin
  password: secure-random-password-here
---
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: production-repo
  namespace: backup-system
spec:
  hostname: prod-cluster
  username: backup-service
  storageType: filesystem

  fileSystemOptions:
    path: /backup/prod
    nfsServer: nfs.example.com
    nfsPath: /exports/backups

  server:
    enabled: true
    exposure:
      type: Ingress
      host: kopia-prod.example.com
      ingressClassName: nginx
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod
        nginx.ingress.kubernetes.io/backend-protocol: "HTTPS"
    adminPasswordExistingSecret: kopia-admin-secret
    resources:
      requests:
        memory: "1Gi"
        cpu: "1000m"
```

Operator will automatically:

1. ✅ Deploy Kopia Server
2. ✅ Create Service (kopia-server-production-repo)
3. ✅ Create Ingress with TLS
4. ✅ Initialize repository on server
5. ✅ Update status with server URL

### Create a Backup (No Changes Required!)

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: db-backup
  namespace: production
spec:
  pvcName: postgres-data
  repository: production-repo
  schedule: "0 2 * * *"
```

Operator will automatically:

1. ✅ Wait for server readiness
2. ✅ Create user on server (username: production-postgres-data)
3. ✅ Generate secure password
4. ✅ Store credentials in Secret (db-backup-kopia-creds)
5. ✅ Create CronJob that connects to server
6. ✅ Update status with connection info

### Verify Setup

```bash
# Check server deployment
kubectl get deployment -n backup-system kopia-server-production-repo

# Check server service
kubectl get svc -n backup-system kopia-server-production-repo

# Check ingress
kubectl get ingress -n backup-system kopia-server-production-repo

# Check backup credentials
kubectl get secret -n production db-backup-kopia-creds

# View backup status
kubectl get kopiabackup -n production db-backup -o yaml

# Check logs
kubectl logs -n backup-system deployment/kopia-server-production-repo
```

## Migration Path for Existing Installations

### Option 1: Gradual Migration (Recommended)

```bash
# 1. Deploy new operator version
kubectl apply -f dist/install.yaml

# 2. Enable server mode on specific repository
kubectl patch kopiarepository my-repo -n backup-system --type=merge -p '
{
  "spec": {
    "server": {
      "enabled": true,
      "exposure": {
        "type": "Ingress",
        "host": "kopia-my-repo.example.com",
        "ingressClassName": "nginx"
      },
      "adminPasswordExistingSecret": "kopia-admin-secret"
    }
  }
}'

# 3. Operator reconciles and deploys server
# 4. Existing backups are automatically migrated
# 5. Verify backups work with server
# 6. Repeat for other repositories
```

### Option 2: Direct Mode Support (Backward Compatibility)

Repositories without `server.enabled: true` continue to work in direct mode.

```yaml
# This continues to work as before
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: legacy-repo
spec:
  hostname: cluster
  username: backup
  storageType: filesystem
  # server not specified - defaults to direct mode
  fileSystemOptions:
    path: /backup/legacy
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
```

## Key Files to Modify

### API Changes

- `api/backup/v1alpha1/kopiarepository_types.go` - Add Server structs
- `api/backup/v1alpha1/kopiabackup_types.go` - Add status fields

### New Components

- `internal/controller/backup/kopiaserver_manager.go` - Server lifecycle
- `internal/controller/backup/kopiauser_manager.go` - User management
- `internal/controller/backup/kopiaserver_deployment.go` - Deployment templates

### Controller Updates

- `internal/controller/backup/kopiarepository_controller.go` - Add server logic
- `internal/controller/backup/kopiabackup_controller.go` - Add user creation
- `internal/controller/backup/kopiabackup_controller.go` - Update CronJob construction

### RBAC

- `config/rbac/role.yaml` - Add new permissions

### Documentation

- `ARCHITECTURE.md` - Add server mode architecture
- `docs/EXAMPLES.md` - Add server mode examples
- `docs/SERVER_MODE.md` - New detailed guide

## Testing Strategy

### Unit Tests

```bash
# Run unit tests
make test

# Key test files:
# - kopiaserver_manager_test.go
# - kopiauser_manager_test.go
# - kopiabackup_controller_test.go (updated)
# - kopiarepository_controller_test.go (updated)
```

### Integration Tests

```bash
# Run e2e tests
make test-e2e

# Test scenarios:
# - Server deployment and readiness
# - User creation and deletion
# - Backup with server mode
# - Migration from direct to server mode
```

### Manual Testing

```bash
# 1. Create repository with server
# 2. Wait for server deployment
# 3. Create backup
# 4. Verify user creation
# 5. Trigger backup manually
# 6. Check server logs
# 7. Verify snapshot creation
```

## Security Considerations

### Secrets Management

1. **Admin Credentials**: Required for operator to manage users

   ```bash
   kubectl create secret generic kopia-admin-secret \
     --namespace backup-system \
     --from-literal=username=admin \
     --from-literal=password=$(openssl rand -base64 32)
   ```

2. **User Credentials**: Auto-generated by operator per backup

   - Stored in `<backup-name>-kopia-creds` Secret
   - Scoped to backup namespace
   - Automatically rotated on changes

3. **TLS Certificates**:
   - Auto-generated self-signed (development)
   - cert-manager integration (production)
   - Custom CA support

### Network Policies

```yaml
# Example: Restrict server access
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: kopia-server-policy
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: kopia-server
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              backup.cloudinfra.be/enabled: "true"
      ports:
        - protocol: TCP
          port: 51515
```

## Monitoring

### Metrics to Watch

- Server pod status
- Backup job success rate
- Active users count
- Storage usage
- API error rates

### Prometheus Integration

```yaml
# ServiceMonitor example
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: kopia-server
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: kopia-server
  endpoints:
    - port: metrics
      interval: 30s
```

## Next Steps

1. **Review**: Read [MIGRATION_PLAN_SERVER_MODE.md](./MIGRATION_PLAN_SERVER_MODE.md)
2. **Discuss**: Review open questions and architectural decisions
3. **Prototype**: Start with Phase 1 (CRD updates)
4. **Iterate**: Build incrementally with tests
5. **Document**: Update docs as you implement
6. **Test**: Comprehensive testing before release
7. **Migrate**: Gradual migration for existing users

## Questions?

Key decision points to discuss:

- Server HA strategy (single vs multi-replica)
- Certificate management approach
- Migration timeline
- Backward compatibility duration
- External auth integration
- Cross-namespace server access

## Summary

This migration brings significant improvements:

✅ Better security (no storage creds in backup pods)
✅ Centralized control and monitoring
✅ Per-backup access control
✅ Simplified network policies
✅ Better audit trail
✅ Easier troubleshooting

The implementation is designed to be incremental, backward compatible, and production-ready.
