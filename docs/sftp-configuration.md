# SFTP Configuration Guide

This guide explains how to configure SFTP storage for Kopia repositories using direct API fields instead of ConfigMaps.

## Overview

SFTP configuration is now embedded directly in the `KopiaRepository` spec, with sensitive credentials (username, password, SSH key) stored in a Kubernetes Secret.

## Configuration Structure

### 1. Create SFTP Credentials Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kopia-sftp-credentials
  namespace: default
type: Opaque
stringData:
  # Required: SFTP username
  username: "backup-user"

  # Option 1: Use password authentication
  password: "your-sftp-password"

  # Option 2: Use SSH key authentication (preferred)
  keyData: |
    -----BEGIN OPENSSH PRIVATE KEY----- #gitleaks:allow
    b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAABlwAAAAdzc2gtcn
    ... your complete SSH private key ...
    -----END OPENSSH PRIVATE KEY-----
```

**Note**: You must provide either `password` OR `keyData`. SSH key authentication is recommended for security.

### 2. Configure KopiaRepository

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: sftprepo
  namespace: default
spec:
  hostname: kopia-server
  username: kopia
  storageType: sftp
  repositoryPassword: "your-repository-encryption-password"
  defaultSchedule: "0 2 * * *"

  # SFTP configuration
  sftpOptions:
    # Required fields
    host: "sftp.example.com" # SFTP server hostname
    path: "/backups/kopia-repo" # Path on SFTP server
    credentialsSecret: kopia-sftp-credentials # Reference to secret above

    # Optional fields
    port: 22 # Default: 22
    knownHostsData: | # SSH known_hosts for host verification
      sftp.example.com ssh-rsa AAAAB3NzaC1yc2EAAAABI...
      sftp.example.com ecdsa-sha2-nistp256 AAAAE2VjZHNh...
    externalSSH: false # Use external SSH command
    sshCommand: "ssh" # SSH command when externalSSH=true
    dirShards: [] # Directory sharding configuration

  # Optional: Enable Kopia Server mode
  server:
    enabled: true
    image: "ghcr.io/fastlorenzo/kopia:latest"
    replicas: 1
    exposure:
      type: Service
      serviceType: ClusterIP
      servicePort: 51515
```

## Field Descriptions

### Required Fields

- **`sftpOptions.host`**: SFTP server hostname or IP address
- **`sftpOptions.path`**: Path to the repository on the SFTP server
- **`sftpOptions.credentialsSecret`**: Name of the Secret containing SFTP credentials

### Optional Fields

- **`sftpOptions.port`**: SFTP port (default: 22)
- **`sftpOptions.knownHostsData`**: SSH known_hosts entries for host key verification
  - Recommended for security to prevent man-in-the-middle attacks
  - Can be obtained with: `ssh-keyscan -t rsa,ecdsa,ed25519 sftp.example.com`
- **`sftpOptions.externalSSH`**: Use external SSH command instead of built-in (default: false)
- **`sftpOptions.sshCommand`**: Custom SSH command when `externalSSH` is true (default: "ssh")
- **`sftpOptions.dirShards`**: Directory sharding configuration for repository layout

## Authentication Methods

### SSH Key Authentication (Recommended)

1. Generate SSH key pair:

   ```bash
   ssh-keygen -t ed25519 -C "kopia-backup" -f kopia_sftp_key
   ```

2. Copy public key to SFTP server:

   ```bash
   ssh-copy-id -i kopia_sftp_key.pub backup-user@sftp.example.com
   ```

3. Create Secret with private key:
   ```bash
   kubectl create secret generic kopia-sftp-credentials \
     --from-literal=username=backup-user \
     --from-file=keyData=kopia_sftp_key
   ```

### Password Authentication

```bash
kubectl create secret generic kopia-sftp-credentials \
  --from-literal=username=backup-user \
  --from-literal=password='your-secure-password'
```

## How It Works

### Server Mode

When server mode is enabled, the operator:

1. Mounts the credentials secret at `/sftp-creds/` in the Kopia Server pod
2. Reads `username`, `password`, and/or `keyData` from the secret
3. Builds the appropriate `kopia repository connect sftp` command with:
   - Host, port, path from the spec
   - Username from secret
   - SSH key or password from secret
   - Optional known_hosts data
4. Connects to the SFTP repository before starting the server

### Direct Mode (CronJob)

For direct backup jobs (when server mode is disabled):

1. The CronJob mounts the SFTP credentials secret
2. Each backup job connects directly to the SFTP server using the credentials
3. Snapshots are created and uploaded via SFTP

## Security Best Practices

1. **Use SSH key authentication** instead of passwords
2. **Provide knownHostsData** to prevent MITM attacks
3. **Restrict SFTP user permissions** to only the backup path
4. **Use strong repository passwords** for encryption
5. **Enable RBAC** to restrict Secret access
6. **Rotate credentials periodically**

## Migration from ConfigMap

If you were previously using ConfigMap-based configuration:

**Old (ConfigMap):**

```yaml
sftpOptions:
  configMapName: kopia-sftp-config
```

**New (Direct + Secret):**

```yaml
sftpOptions:
  host: "sftp.example.com"
  port: 22
  path: "/backups/kopia-repo"
  credentialsSecret: kopia-sftp-credentials
  knownHostsData: "..."
```

## Troubleshooting

### Connection Failures

Check the Kopia Server logs:

```bash
kubectl logs -n default deployment/kopia-server-sftprepo -c kopia-server
```

Common issues:

- **"username not found in secret"**: Check that the Secret has a `username` key
- **"Neither keyData nor password found"**: Provide at least one authentication method
- **"Host key verification failed"**: Add `knownHostsData` with the server's SSH host keys
- **"Permission denied"**: Verify SFTP user has access to the path

### Verify Secret Contents

```bash
kubectl get secret kopia-sftp-credentials -o jsonpath='{.data}' | jq
```

### Test SFTP Connection

```bash
# With password
sftp -P 22 backup-user@sftp.example.com

# With SSH key
sftp -i /path/to/key -P 22 backup-user@sftp.example.com
```

## Example: Complete Setup

```bash
# 1. Create SSH key
ssh-keygen -t ed25519 -f kopia_sftp_key -N ""

# 2. Copy to SFTP server
ssh-copy-id -i kopia_sftp_key.pub backup-user@sftp.example.com

# 3. Get known_hosts
ssh-keyscan -t ed25519 sftp.example.com > known_hosts

# 4. Create Secret
kubectl create secret generic kopia-sftp-credentials \
  --from-literal=username=backup-user \
  --from-file=keyData=kopia_sftp_key

# 5. Create KopiaRepository (see example above)
kubectl apply -f kopiarepository.yaml

# 6. Verify server is running
kubectl get pods -l kopia-repository=sftprepo
kubectl logs deployment/kopia-server-sftprepo
```
