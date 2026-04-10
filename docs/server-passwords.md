# Kopia Server Password Architecture

## Overview

The Kopia operator implements a security-focused dual-password architecture when running in server mode:

1. **Repository Encryption Password** (`passwordSecretName`) - Encrypts/decrypts repository data
2. **Server Admin Password** (`adminPasswordSecretName`) - Controls server API access

## Password Purposes

### Repository Encryption Password

- **Purpose**: Encrypts and decrypts the actual backup data in the repository
- **Environment Variable**: `KOPIA_PASSWORD`
- **Used For**:
  - Repository initialization and connection
  - Data encryption/decryption
  - Creating per-backup user accounts (each backup user gets the repository password)

### Server Admin Password

- **Purpose**: Controls administrative access to the Kopia Server API
- **Environment Variable**: `KOPIA_SERVER_PASSWORD`
- **Used For**:
  - Starting the server with `--server-control-password`
  - Server status checks
  - User management operations (creating/updating backup users)

## Security Benefits

**Separation of Concerns**: The server admin password controls who can manage the server, while the repository password controls who can access encrypted data.

**Reduced Attack Surface**: If the server admin password is compromised, an attacker gains server management access but cannot decrypt the repository data without the repository password.

**User Isolation**: Per-backup users authenticate with the repository password to access their data, while server management remains separate.

## Configuration

### Using Secret References

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: my-repo
spec:
  passwordSecretName: "kopia-repo-password"
  server:
    enabled: true
    adminPasswordSecretName: "kopia-server-admin-password"
```

The secrets must contain a key named `password`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kopia-repo-password
stringData:
  password: "your-repository-encryption-password"
---
apiVersion: v1
kind: Secret
metadata:
  name: kopia-server-admin-password
stringData:
  password: "your-server-admin-password"
```

## Default Behavior

If `adminPasswordSecretName` is not specified:

- The operator falls back to using `passwordSecretName` for both purposes
- This maintains backward compatibility but is less secure

**Recommendation**: Always specify separate passwords for production deployments.

## Example: Complete Setup

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: kopia-repo-password
stringData:
  password: "very-secure-encryption-password-123"
---
apiVersion: v1
kind: Secret
metadata:
  name: kopia-server-admin-password
stringData:
  password: "different-admin-password-456"
---
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: production-repo
spec:
  hostname: kopia-server
  username: kopia
  storageType: filesystem
  passwordSecretName: kopia-repo-password
  fileSystemOptions:
    path: /backup/kopia
  server:
    enabled: true
    adminPasswordSecretName: kopia-server-admin-password
    image: "ghcr.io/fastlorenzo/kopia:latest"
    exposure:
      type: Service
      serviceType: ClusterIP
```

## Password Rotation

To rotate passwords:

1. **Server Admin Password**:

   - Update the Secret referenced by `.spec.server.adminPasswordSecretName`
   - The operator will update the deployment and recreate pods
   - No impact on existing backups or data access

2. **Repository Encryption Password**:
   - ⚠️ **WARNING**: Changing this requires re-encryption of the entire repository
   - Not recommended for production repositories
   - Consider creating a new repository instead

## Troubleshooting

### Server Won't Start

- Check that `KOPIA_SERVER_PASSWORD` is correctly set in the deployment
- Verify the server admin password secret exists and has the correct key

### Users Can't Connect

- Check that per-backup users are created with `KOPIA_PASSWORD` (repository password)
- Verify backup CronJobs mount the correct user credentials secret

### Permission Denied Errors

- Ensure server control operations use `--server-password=${KOPIA_SERVER_PASSWORD}`
- Verify user operations use the repository password
