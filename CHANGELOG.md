# Changelog

All notable changes to the Kopia Operator will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Initial public release of Kopia Operator
- `KopiaRepository` CRD for managing Kopia backup repositories
- `KopiaBackup` CRD for scheduling PVC backups
- **Storage backends:**
  - Filesystem (NFS) support
  - SFTP support with password and SSH key authentication
- **Operating modes:**
  - Direct mode: Backup jobs connect directly to storage
  - Server mode: Centralized Kopia Server with per-backup user isolation
- Server mode features:
  - Automatic Kopia Server deployment and management
  - Per-backup user creation and credential management
  - TLS certificate management
  - Service and Ingress exposure options
- CronJob-based backup scheduling
- Automatic status reporting and condition management
- Retention policy configuration
- Resource limits configuration for backup jobs

### Documentation

- Architecture documentation with visual diagrams
- SFTP configuration guide
- Server mode password management guide
- Example configurations for common use cases

## [0.1.0] - Initial Development

- Internal development version
- Core operator functionality implementation
