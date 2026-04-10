# Kopia Operator Examples

This document provides practical examples for using the Kopia Operator.

## Table of Contents

- [Basic Setup](#basic-setup)
- [Filesystem Storage Examples](#filesystem-storage-examples)
- [SFTP Storage Examples](#sftp-storage-examples)
- [Manual Backup Configuration](#manual-backup-configuration)
- [Automatic Backup via PVC Labels](#automatic-backup-via-pvc-labels)
- [Advanced Configurations](#advanced-configurations)

## Basic Setup

### Prerequisites

1. Kubernetes cluster (v1.11.3+)
2. kubectl configured
3. Storage backend (NFS server or SFTP server)
4. Kopia repository initialized (or let the operator create it)

### Install the Operator

```bash
# Install CRDs
make install

# Deploy the operator
make deploy IMG=<your-registry>/kopia-operator:tag

# Or apply pre-built manifests
kubectl apply -f dist/install.yaml
```

## Filesystem Storage Examples

### Example 1: Basic NFS Repository

Create a repository that stores backups on an NFS share:

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: nfs-backup-repo
  namespace: backup-system
spec:
  hostname: k8s-cluster
  username: backup-user
  storageType: filesystem
  description: "Main NFS backup repository"
  enableActions: true
  defaultSchedule: "0 2 * * *" # Daily at 2 AM

  # Use existing secret for password
  passwordSecretName: kopia-repo-password

  # NFS configuration
  fileSystemOptions:
    path: /backup/kopia-repo
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
    fileMode: 0600
    dirMode: 0700

  # Caching configuration
  caching:
    cacheDirectory: /cache
    maxCacheSize: 5242880000 # 5GB
    maxMetadataCacheSize: 5242880000 # 5GB
    maxListCacheDuration: 30
```

Create the password secret:

```bash
kubectl create secret generic kopia-repo-password \
  --namespace backup-system \
  --from-literal=KOPIA_PASSWORD='your-secure-password'
```

## SFTP Storage Examples

### Example 2: SFTP Repository

First, create a ConfigMap with SFTP configuration:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sftp-config
  namespace: backup-system
data:
  repository.config: |
    {
      "storage": {
        "type": "sftp",
        "config": {
          "path": "/backups/kopia",
          "host": "sftp.example.com:22",
          "username": "backup-user",
          "keyfile": "/config/ssh-key",
          "knownHostsFile": "/config/known_hosts"
        }
      },
      "hostname": "k8s-cluster",
      "username": "backup-user"
    }
  ssh-key: |
    -----BEGIN OPENSSH PRIVATE KEY----- #gitleaks:allow
    ... your SSH private key ...
    -----END OPENSSH PRIVATE KEY-----
  known_hosts: |
    sftp.example.com ssh-rsa AAAAB3NzaC1yc2E...
```

Then create the KopiaRepository:

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: sftp-repo
  namespace: backup-system
spec:
  hostname: k8s-cluster
  username: backup-user
  storageType: sftp
  description: "SFTP backup repository"
  enableActions: true
  defaultSchedule: "0 3 * * *" # Daily at 3 AM

  passwordSecretName: kopia-repo-password

  sftpOptions:
    configMapName: sftp-config

  caching:
    cacheDirectory: /cache
    maxCacheSize: 3221225472 # 3GB
```

## Manual Backup Configuration

### Example 4: Backup a Specific PVC

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: postgres-backup
  namespace: database
spec:
  pvcName: postgres-data
  repository: nfs-backup-repo # Reference to KopiaRepository
  schedule: "0 1 * * *" # Daily at 1 AM
  suspend: false
```

### Example 5: Multiple Backups with Different Schedules

```yaml
# Hourly backup for critical data
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: critical-data-hourly
  namespace: production
spec:
  pvcName: critical-app-data
  repository: nfs-backup-repo
  schedule: "0 * * * *" # Every hour
---
# Daily backup for regular data
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: app-data-daily
  namespace: production
spec:
  pvcName: app-data
  repository: nfs-backup-repo
  schedule: "0 2 * * *" # Daily at 2 AM
---
# Weekly backup for archive data
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: archive-weekly
  namespace: production
spec:
  pvcName: archive-data
  repository: nfs-backup-repo
  schedule: "0 3 * * 0" # Weekly on Sunday at 3 AM
```

### Example 6: Suspended Backup

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: maintenance-backup
  namespace: default
spec:
  pvcName: maintenance-data
  repository: nfs-backup-repo
  schedule: "0 4 * * *"
  suspend: true # Backup is suspended, won't run
```

## Automatic Backup via PVC Labels

### Example 7: Auto-Create Backup by Labeling PVC

Simply add a label to your PVC:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-app-data
  namespace: default
  labels:
    backup.cloudinfra.be/repository: nfs-backup-repo
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: standard
```

The operator will automatically:

1. Detect the label on the PVC
2. Create a KopiaBackup resource named `my-app-data`
3. Use the default schedule from the repository
4. Set the backup status to `FromAnnotation: true`

To disable the backup, simply remove the label:

```bash
kubectl label pvc my-app-data backup.cloudinfra.be/repository-
```

The operator will automatically delete the associated KopiaBackup.

### Example 8: Complete Application with Auto-Backup

```yaml
# Application deployment
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wordpress
  namespace: web
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: wordpress
  template:
    metadata:
      labels:
        app.kubernetes.io/name: wordpress
    spec:
      containers:
        - name: wordpress
          image: wordpress:latest
          volumeMounts:
            - name: wordpress-data
              mountPath: /var/www/html
      volumes:
        - name: wordpress-data
          persistentVolumeClaim:
            claimName: wordpress-data
---
# PVC with automatic backup
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: wordpress-data
  namespace: web
  labels:
    app.kubernetes.io/name: wordpress
    backup.cloudinfra.be/repository: nfs-backup-repo
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 20Gi
  storageClassName: nfs-client
```

## Advanced Configurations

### Example 9: Multi-Namespace Repository

Create a repository in a central namespace that can be referenced from any namespace:

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: shared-repo
  namespace: backup-system
spec:
  hostname: k8s-cluster
  username: shared-backup
  storageType: filesystem
  passwordSecretName: shared-repo-password
  defaultSchedule: "0 2 * * *"

  fileSystemOptions:
    path: /backup/shared
    nfsServer: nfs.example.com
    nfsPath: /exports/shared-backups
```

Use it from different namespaces:

```yaml
# In namespace 'production'
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: prod-db
  namespace: production
spec:
  pvcName: database-data
  repository: shared-repo # References backup-system/shared-repo
  schedule: "0 1 * * *"
---
# In namespace 'development'
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: dev-db
  namespace: development
spec:
  pvcName: database-data
  repository: shared-repo # References the same repository
  schedule: "0 3 * * *"
```

### Example 10: Custom Caching Configuration

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: high-performance-repo
  namespace: backup-system
spec:
  hostname: k8s-cluster
  username: backup-user
  storageType: filesystem
  passwordSecretName: kopia-repo-password

  fileSystemOptions:
    path: /backup/high-perf
    nfsServer: nfs.example.com
    nfsPath: /exports/fast-storage

  # Custom caching for better performance
  caching:
    cacheDirectory: /cache
    maxCacheSize: 10737418240 # 10GB
    maxMetadataCacheSize: 10737418240 # 10GB
    maxListCacheDuration: 60 # 60 seconds
    minMetadataSweepAge: 3600 # 1 hour
    minContentSweepAge: 3600 # 1 hour
```

### Example 11: Read-Only Repository

Useful for restore-only scenarios:

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: readonly-repo
  namespace: restore-system
spec:
  hostname: k8s-cluster
  username: restore-user
  storageType: filesystem
  readOnly: true
  permissiveCacheLoading: true
  passwordSecretName: kopia-repo-password

  fileSystemOptions:
    path: /backup/readonly
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
```

## Monitoring and Troubleshooting

### Check Backup Status

```bash
# List all backups
kubectl get kopiabackup -A

# Get detailed backup info
kubectl describe kopiabackup postgres-backup -n database

# Check CronJob created by operator
kubectl get cronjob -n database

# View recent backup jobs
kubectl get jobs -n database | grep snapshot-

# Check backup logs
kubectl logs -n database <snapshot-job-pod>
```

### Verify Repository

```bash
# List repositories
kubectl get kopiarepository -A

# Get repository details
kubectl describe kopiarepository nfs-backup-repo -n backup-system

# Check repository ConfigMap
kubectl get configmap -n backup-system | grep kopia-config
```

### Common Issues

**Backup not running:**

```bash
# Check if backup is suspended
kubectl get kopiabackup <name> -n <namespace> -o jsonpath='{.spec.suspend}'

# Check if PVC exists
kubectl get pvc -n <namespace>

# Check if pod is running with PVC mounted
kubectl get pods -n <namespace> -o wide
```

**Repository connection issues:**

```bash
# Check ConfigMap
kubectl get configmap kopia-config-<repo-name> -n <namespace> -o yaml

# Check secret exists
kubectl get secret <secret-name> -n <namespace>

# Check NFS mount (for filesystem storage)
kubectl run -it nfs-test --image=busybox --rm -- sh
# Inside pod:
mount | grep nfs
```

## Schedule Format Reference

The `schedule` field uses standard cron format:

```text
┌───────────── minute (0 - 59)
│ ┌───────────── hour (0 - 23)
│ │ ┌───────────── day of month (1 - 31)
│ │ │ ┌───────────── month (1 - 12)
│ │ │ │ ┌───────────── day of week (0 - 6) (Sunday=0)
│ │ │ │ │
│ │ │ │ │
* * * * *
```

**Common Schedules:**

- `"0 2 * * *"` - Daily at 2 AM
- `"0 */6 * * *"` - Every 6 hours
- `"0 0 * * 0"` - Weekly on Sunday at midnight
- `"0 0 1 * *"` - Monthly on the 1st at midnight
- `"*/30 * * * *"` - Every 30 minutes
- `"0 9-17 * * 1-5"` - Every hour from 9 AM to 5 PM, Monday through Friday

## Best Practices

1. **Use Secrets for Passwords**: Always use Kubernetes Secrets referenced via `passwordSecretName` — never embed passwords in manifests
2. **Centralized Repositories**: Create repositories in a dedicated namespace for easier management
3. **Appropriate Schedules**: Set backup schedules during low-traffic periods
4. **Monitor Resources**: Keep an eye on storage usage and cache sizes
5. **Test Restores**: Regularly test backup restores to ensure data integrity
6. **Label Your PVCs**: Use the label-based approach for automatic backup management
7. **Use Meaningful Names**: Name backups and repositories descriptively
8. **Set Suspends Wisely**: Use `suspend: true` during maintenance, not as a permanent state
9. **Resource Limits**: Consider setting resource limits on backup CronJobs for large datasets
10. **Retention Policies**: Configure Kopia retention policies in your repository

## Cleanup

### Remove a Backup

```bash
# Delete KopiaBackup (automatically deletes CronJob)
kubectl delete kopiabackup <name> -n <namespace>
```

### Remove a Repository

```bash
# Delete repository (check for dependent backups first)
kubectl delete kopiarepository <name> -n <namespace>
```

### Uninstall Operator

```bash
# Remove all instances
kubectl delete -k config/samples/

# Remove operator
make undeploy

# Remove CRDs
make uninstall
```
