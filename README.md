# Kopia Operator

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/fastlorenzo/kopia-operator)](https://goreportcard.com/report/github.com/fastlorenzo/kopia-operator)

A Kubernetes operator for managing [Kopia](https://kopia.io/) backup operations for Persistent Volume Claims (PVCs).

## Features

- **Automated PVC Backups**: Schedule backups for PVCs using Kubernetes CronJobs
- **Annotation-based Discovery**: Automatically create backups for PVCs with the `backup.cloudinfra.be/repository` label
- **Server Mode**: Deploy a centralized Kopia Server for enhanced security and management
- **Direct Mode**: Connect directly to storage backends (NFS, SFTP) for simpler setups
- **Multiple Storage Backends**: Support for filesystem (NFS) and SFTP storage
- **Per-backup User Isolation**: Each backup gets its own credentials when using server mode

## Architecture

The operator uses two Custom Resource Definitions (CRDs):

- **KopiaRepository**: Defines the backup destination (storage backend, credentials, server configuration)
- **KopiaBackup**: Defines what to backup (PVC reference, schedule, repository reference)

For detailed architecture information, see [ARCHITECTURE.md](ARCHITECTURE.md).

## Quick Start

### Prerequisites

- Kubernetes cluster v1.24+
- kubectl configured to access your cluster
- A storage backend (NFS share or SFTP server)

### Installation

1. **Install CRDs:**

   ```sh
   make install
   ```

2. **Deploy the operator:**

   ```sh
   make deploy IMG=ghcr.io/fastlorenzo/kopia-operator:latest
   ```

### Create a Repository

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: my-backup-repo
spec:
  hostname: kopia-host
  username: kopia-user
  storageType: filesystem
  repositoryPasswordExistingSecret: kopia-password-secret
  fileSystemOptions:
    path: /backups
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
```

### Create a Backup

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: my-pvc-backup
spec:
  pvcName: my-data-pvc
  repository: my-backup-repo
  schedule: "0 2 * * *"  # Daily at 2 AM
```

### Auto-discovery with Labels

Add a label to your PVC to automatically create backups:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-data-pvc
  labels:
    backup.cloudinfra.be/repository: my-backup-repo
spec:
  # ... PVC spec
```

## Server Mode

For enhanced security, you can enable server mode which deploys a centralized Kopia Server:

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: my-server-repo
spec:
  hostname: kopia-host
  username: kopia-user
  storageType: filesystem
  repositoryPasswordExistingSecret: kopia-password-secret
  server:
    enabled: true
    image: ghcr.io/fastlorenzo/kopia:0.20.1
  fileSystemOptions:
    path: /backups
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
```

Benefits of server mode:
- Storage credentials only exist on the server
- Per-backup user isolation
- Centralized monitoring and logging
- TLS encryption for all connections

## Development

### Prerequisites

- Go 1.21+
- Docker
- kubectl
- Access to a Kubernetes cluster

### Build

```sh
make build
```

### Run locally

```sh
make run
```

### Run tests

```sh
make test
```

## Documentation

- [Architecture](ARCHITECTURE.md)
- [SFTP Configuration](docs/sftp-configuration.md)
- [Server Passwords](docs/server-passwords.md)
- [Examples](docs/EXAMPLES.md)

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Disclaimer

This project is provided as-is. Please test thoroughly before using in production.
Always maintain separate backups and verify your backup strategy.

## License

Copyright 2024-2025.

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
