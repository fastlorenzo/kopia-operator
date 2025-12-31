# Kopia Operator Server Mode - Visual Architecture

## Current Architecture (Direct Storage)

```
┌───────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes Cluster                              │
│                                                                           │
│  ┌────────────────────────────────────────────────────────────────────┐   │
│  │ Namespace: default                                                 │   │
│  │                                                                    │   │
│  │  ┌────────────────────┐          ┌────────────────────┐            │   │
│  │  │  Application Pod   │          │  Backup CronJob    │            │   │
│  │  │  ┌──────────────┐  │          │  ┌──────────────┐  │            │   │
│  │  │  │     App      │  │          │  │ Kopia Client │  │            │   │
│  │  │  │              │  │          │  │              │  │            │   │
│  │  │  │ Writes data  │  │          │  │ Has storage  │  │            │   │
│  │  │  │ to PVC       │  │          │  │ credentials  │  │            │   │
│  │  │  └──────┬───────┘  │          │  └──────┬───────┘  │            │   │
│  │  │         │          │          │         │          │            │   │
│  │  │    ┌────▼────┐     │          │    ┌────▼────┐     │            │   │
│  │  │    │   PVC   │◄────┼──────────┼────│   PVC   │     │            │   │
│  │  │    │  (RWO)  │     │          │    │ (ReadOnly)    │            │   │
│  │  │    └─────────┘     │          │    └────┬────┘     │            │   │
│  │  └────────────────────┘          │         │          │            │   │
│  │                                  │         │          │            │   │
│  │                                  │         │ Direct   │            │   │
│  │                                  │         │ Connection            │   │
│  └──────────────────────────────────┼─────────┼──────────┼────────────┘   │
│                                     │         │          │                │
│                                     │         ▼          │                │
│                              ┌──────┴────────────────────┴─────┐          │
│                              │      ConfigMap / Secrets        │          │
│                              │  - NFS Server: nfs.example.com  │          │
│                              │  - NFS Path: /exports/backups   │          │
│                              │  - Repository Password          │          │
│                              │  - SFTP Keys (if SFTP)          │          │
│                              └──────────────┬──────────────────┘          │
│                                             │                             │
└─────────────────────────────────────────────┼─────────────────────────────┘
                                              │
                                              │ Direct Access
                                              │ (Every backup pod has creds)
                                              │
                                              ▼
                         ┌────────────────────────────────────┐
                         │   External Storage (Outside K8s)   │
                         │  ┌──────────────────────────────┐  │
                         │  │  NFS Server / SFTP Server    │  │
                         │  │  /exports/backups/           │  │
                         │  │    ├── kopia.repository.f    │  │
                         │  │    ├── snapshots/            │  │
                         │  │    └── ...                   │  │
                         │  └──────────────────────────────┘  │
                         └────────────────────────────────────┘

Issues with Current Architecture:
❌ Every backup pod needs storage credentials (security risk)
❌ Storage directly exposed to all backup pods (network complexity)
❌ No centralized access control or user management
❌ Difficult to audit who accessed what
❌ Network policies must allow all backup pods to access storage
❌ Credential rotation affects all backup pods
```

## Target Architecture (Kopia Server Mode)

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           Kubernetes Cluster                             │
│                                                                          │
│  ┌───────────────────────────────────────────────────────────────────┐   │
│  │ Namespace: default (Application Namespace)                        │   │
│  │                                                                   │   │
│  │  ┌────────────────────┐          ┌────────────────────┐           │   │
│  │  │  Application Pod   │          │  Backup CronJob    │           │   │
│  │  │  ┌──────────────┐  │          │  ┌──────────────┐  │           │   │
│  │  │  │     App      │  │          │  │ Kopia Client │  │           │   │
│  │  │  │              │  │          │  │              │  │           │   │
│  │  │  │ Writes data  │  │          │  │ Only has     │  │           │   │
│  │  │  │ to PVC       │  │          │  │ user creds   │  │           │   │
│  │  │  └──────┬───────┘  │          │  └──────┬───────┘  │           │   │
│  │  │         │          │          │         │          │           │   │
│  │  │    ┌────▼────┐     │          │    ┌────▼────┐     │           │   │
│  │  │    │   PVC   │◄────┼──────────┼────│   PVC   │     │           │   │
│  │  │    │  (RWO)  │     │          │    │ (ReadOnly)    │           │   │
│  │  │    └─────────┘     │          │    └────┬────┘     │           │   │
│  │  └────────────────────┘          │         │          │           │   │
│  │                                  │         │          │           │   │
│  │                             ┌────┴─────────▼──────────┴────┐      │   │
│  │                             │         Secret               │      │   │
│  │                             │  default-myapp-kopia-creds   │      │   │
│  │                             │  - username: default-myapp   │      │   │
│  │                             │  - password: *************** │      │   │
│  │                             └───────────────┬──────────────┘      │   │
│  └─────────────────────────────────────────────┼─────────────────────┘   │
│                                                │                         │
│                                                │ HTTPS API               │
│                                                │ (User credentials)      │
│  ┌─────────────────────────────────────────────▼─────────────────────┐   │
│  │ Namespace: backup-system (Centralized Backup Infrastructure)      │   │
│  │                                                                   │   │
│  │  ┌──────────────────────────────────────────────────────────┐     │   │
│  │  │           Kopia Server Deployment                        │     │   │
│  │  │  ┌────────────────────────────────────────────────────┐  │     │   │
│  │  │  │  Pod: kopia-server-prod-repo-xxxxx                 │  │     │   │
│  │  │  │  ┌───────────────────────────────────────────────┐ │  │     │   │
│  │  │  │  │        Kopia Server Process                   │ │  │     │   │
│  │  │  │  │  ┌─────────────────────────────────────────┐  │ │  │     │   │
│  │  │  │  │  │ User Management                         │  │ │  │     │   │
│  │  │  │  │  │  - default-myapp (password)             │  │ │  │     │   │
│  │  │  │  │  │  - production-db (password)             │  │ │  │     │   │
│  │  │  │  │  │  - staging-cache (password)             │  │ │  │     │   │
│  │  │  │  │  │  ...                                    │  │ │  │     │   │
│  │  │  │  │  └─────────────────────────────────────────┘  │ │  │     │   │
│  │  │  │  │  ┌─────────────────────────────────────────┐  │ │  │     │   │
│  │  │  │  │  │ API Server (HTTPS on :51515)            │  │ │  │     │   │
│  │  │  │  │  │  - /api/v1/snapshot                     │  │ │  │     │   │
│  │  │  │  │  │  - /api/v1/repo/status                  │  │ │  │     │   │
│  │  │  │  │  │  - /api/v1/users                        │  │ │  │     │   │
│  │  │  │  │  └─────────────────────────────────────────┘  │ │  │     │   │
│  │  │  │  │  ┌─────────────────────────────────────────┐  │ │  │     │   │
│  │  │  │  │  │ Storage Backend Connection              │  │ │  │     │   │
│  │  │  │  │  │  - Has storage credentials              │  │ │  │     │   │
│  │  │  │  │  │  - Manages repository                   │  │ │  │     │   │
│  │  │  │  │  └─────────────────┬───────────────────────┘  │ │  │     │   │
│  │  │  │  └──────────────────────┼────────────────────────┘ │  │     │   │
│  │  │  │           ┌───────────▼────────────┐               │  │     │   │
│  │  │  │           │   Volume Mount         │               │  │     │   │
│  │  │  │           │   - NFS Mount (or)     │               │  │     │   │
│  │  │  │           │   - SFTP Config        │               │  │     │   │
│  │  │  │           └────────────────────────┘               │  │     │   │
│  │  │  └────────────────────────────────────────────────────┘  │     │   │
│  │  │                                                          │     │   │
│  │  └──────────────────┬───────────────────────────────────────┘     │   │
│  │                     │                                             │   │
│  │                ┌────▼────┐                                        │   │
│  │                │ Service │                                        │   │
│  │                │ ClusterIP                                        │   │
│  │                │ :51515  │                                        │   │
│  │                └────┬────┘                                        │   │
│  │                     │                                             │   │
│  │                ┌────▼────────────────────────────┐                │   │
│  │                │        Ingress / HTTPRoute      │                │   │
│  │                │  Host: kopia-prod.example.com   │                │   │
│  │                │  TLS: letsencrypt-prod          │                │   │
│  │                └────┬────────────────────────────┘                │   │
│  └─────────────────────┼─────────────────────────────────────────────┘   │
│                        │                                                 │
└────────────────────────┼─────────────────────────────────────────────────┘
                         │
                         │ HTTPS (Only server has storage creds)
                         │
                         ▼
    ┌────────────────────────────────────┐
    │   External Storage (Outside K8s)   │
    │  ┌──────────────────────────────┐  │
    │  │  NFS Server / SFTP Server    │  │
    │  │  /exports/backups/           │  │
    │  │    ├── kopia.repository.f    │  │
    │  │    ├── snapshots/            │  │
    │  │    │   └── default-myapp/    │  │
    │  │    │   └── production-db/    │  │
    │  │    └── ...                   │  │
    │  └──────────────────────────────┘  │
    └────────────────────────────────────┘

Benefits of New Architecture:
✅ Only Kopia Server has storage credentials (1 place vs N places)
✅ Per-backup user authentication and authorization
✅ Centralized audit logging and monitoring
✅ Simplified network policies (only server → storage)
✅ Easy credential rotation (only update server)
✅ HTTPS API with TLS encryption
✅ Ingress integration for external access
✅ Better compliance and security posture
```

## Component Interaction Flow

```
┌──────────────────────────────────────────────────────────────────────────┐
│  Component Lifecycle and Interaction Flow                                │
└──────────────────────────────────────────────────────────────────────────┘

1. Repository Creation
   ┌─────────────────────────────────────────────────────────────────────┐
   │ User creates KopiaRepository with server.enabled: true              │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ KopiaRepositoryReconciler                                           │
   │  1. Validates repository spec                                       │
   │  2. Creates admin secret (if not exists)                            │
   │  3. Calls KopiaServerManager.EnsureServerDeployment()               │
   │  4. Calls KopiaServerManager.EnsureServerService()                  │
   │  5. Calls KopiaServerManager.EnsureServerExposure()                 │
   │  6. Waits for server readiness                                      │
   │  7. Initializes repository on server                                │
   │  8. Updates status.serverURL                                        │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ Kubernetes Resources Created:                                       │
   │  ✅ Deployment: kopia-server-<repo-name>                            │
   │  ✅ Service: kopia-server-<repo-name>                               │
   │  ✅ Ingress/HTTPRoute: kopia-server-<repo-name>                     │
   │  ✅ ConfigMap: kopia-config-<repo-name> (optional)                  │
   │  ✅ Secret: <repo-name>-tls (if auto-generated)                     │
   └─────────────────────────────────────────────────────────────────────┘

2. Backup Creation
   ┌─────────────────────────────────────────────────────────────────────┐
   │ User creates KopiaBackup                                            │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ KopiaBackupReconciler                                               │
   │  1. Validates backup spec                                           │
   │  2. Fetches referenced KopiaRepository                              │
   │  3. Waits for repository.status.serverReady == true                 │
   │  4. Calls KopiaUserManager.EnsureUser()                             │
   │     └─> Creates user on Kopia Server via API                        │
   │     └─> Generates secure password                                   │
   │     └─> Stores credentials in Secret                                │
   │  5. Discovers runtime info (node, app, pod)                         │
   │  6. Creates/Updates CronJob with server connection                  │
   │  7. Updates backup status                                           │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ Kubernetes Resources Created:                                       │
   │  ✅ Secret: <backup-name>-kopia-creds                               │
   │     - username: <namespace>-<pvcname>                               │
   │     - password: <generated-secure-password>                         │
   │  ✅ CronJob: snapshot-<pvcname>                                     │
   │     - Env: KOPIA_SERVER_URL, KOPIA_SERVER_USERNAME, PASSWORD        │
   │     - Command: kopia server login && kopia snapshot create ...      │
   └─────────────────────────────────────────────────────────────────────┘

3. Backup Execution
   ┌─────────────────────────────────────────────────────────────────────┐
   │ CronJob triggers on schedule                                        │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ Backup Pod Execution Flow                                           │
   │  1. Init Container: Random wait (1-900 seconds)                     │
   │  2. Main Container:                                                 │
   │     a. Read credentials from Secret                                 │
   │     b. Connect to server: kopia server login                        │
   │        --url=$KOPIA_SERVER_URL                                      │
   │        --username=$KOPIA_SERVER_USERNAME                            │
   │        --password=$KOPIA_SERVER_PASSWORD                            │
   │     c. Create snapshot: kopia snapshot create /data/...             │
   │     d. List snapshots: kopia snapshot list /data/...                │
   │     e. Show stats: kopia content stats                              │
   │     f. Disconnect: kopia server logout                              │
   │  3. Pod completes successfully                                      │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ Kopia Server Processes Request:                                     │
   │  1. Authenticates user credentials                                  │
   │  2. Authorizes access to snapshot path                              │
   │  3. Receives backup data stream                                     │
   │  4. Deduplicates and compresses data                                │
   │  5. Writes to backend storage (NFS/SFTP)                            │
   │  6. Logs operation with user context                                │
   │  7. Returns success/failure to client                               │
   └─────────────────────────────────────────────────────────────────────┘

4. Backup Deletion
   ┌─────────────────────────────────────────────────────────────────────┐
   │ User deletes KopiaBackup                                            │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ KopiaBackupReconciler Finalizer                                     │
   │  1. Calls KopiaUserManager.DeleteUser()                             │
   │     └─> Deletes user from Kopia Server via API                      │
   │  2. Deletes credentials Secret                                      │
   │  3. CronJob auto-deleted (owner reference)                          │
   │  4. Removes finalizer                                               │
   └─────────────────────────────────────────────────────────────────────┘

5. Repository Deletion
   ┌─────────────────────────────────────────────────────────────────────┐
   │ User deletes KopiaRepository                                        │
   └──────────────────────┬──────────────────────────────────────────────┘
                          │
                          ▼
   ┌─────────────────────────────────────────────────────────────────────┐
   │ KopiaRepositoryReconciler Finalizer                                 │
   │  1. Verifies no dependent KopiaBackups exist                        │
   │  2. Deletes Ingress/HTTPRoute                                       │
   │  3. Deletes Service                                                 │
   │  4. Deletes Deployment (Server pods terminated)                     │
   │  5. Removes finalizer                                               │
   └─────────────────────────────────────────────────────────────────────┘
```

## Security Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│  Security Layers                                                       │
└────────────────────────────────────────────────────────────────────────┘

Layer 1: Network Security
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  ┌──────────────┐        HTTPS/TLS        ┌──────────────┐          │
│  │ Backup Pods  │◄───────────────────────►│ Kopia Server │          │
│  └──────────────┘                         └──────┬───────┘          │
│                                                  │                  │
│  Network Policies:                               │ Only Server      │
│  - Backup pods → Server only                     │ has access       │
│  - Server → Storage only                         ▼                  │
│  - No direct backup pod → storage           ┌──────────────┐        │
│                                             │   Storage    │        │
│                                             └──────────────┘        │
└─────────────────────────────────────────────────────────────────────┘

Layer 2: Authentication & Authorization
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  ┌────────────────────────────────────────────────────────────┐     │
│  │ User Management (on Kopia Server)                          │     │
│  │  - Each backup has unique username/password                │     │
│  │  - Passwords auto-generated (32+ chars)                    │     │
│  │  - Stored in Kubernetes Secrets                            │     │
│  │  - Scoped to backup namespace                              │     │
│  └────────────────────────────────────────────────────────────┘     │
│                                                                     │
│  ┌────────────────────────────────────────────────────────────┐     │
│  │ Access Control                                             │     │
│  │  - Users can only access their own snapshots               │     │
│  │  - Path-based authorization                                │     │
│  │  - Admin user (operator only) for user management          │     │
│  └────────────────────────────────────────────────────────────┘     │
└─────────────────────────────────────────────────────────────────────┘

Layer 3: Credential Management
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Admin Credentials (Operator → Server)                              │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ Secret: kopia-admin-secret                                   │   │
│  │  - username: admin                                           │   │
│  │  - password: <strong-generated-password>                     │   │
│  │  - Used only by operator for user management                 │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  Storage Credentials (Server → Backend)                             │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ Secret: kopia-repo-password                                  │   │
│  │  - KOPIA_PASSWORD: <repository-password>                     │   │
│  │  - Mounted only to server pods                               │   │
│  │  - Not accessible to backup pods                             │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                                                                     │
│  User Credentials (Backup Pod → Server)                             │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ Secret: <backup-name>-kopia-creds                            │   │
│  │  - username: <namespace>-<pvcname>                           │   │
│  │  - password: <auto-generated-secure-password>                │   │
│  │  - Scoped to backup namespace                                │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘

Layer 4: Audit & Logging
┌─────────────────────────────────────────────────────────────────────┐
│                                                                     │
│  Server Audit Logs:                                                 │
│  ✅ User authentication events                                      │
│  ✅ Snapshot creation/deletion                                      │
│  ✅ Failed login attempts                                           │
│  ✅ API access logs                                                 │
│  ✅ User management operations                                      │
│                                                                     │
│  Operator Audit Logs:                                               │
│  ✅ Repository creation/deletion                                    │
│  ✅ Server deployment events                                        │
│  ✅ User creation/deletion                                          │
│  ✅ Configuration changes                                           │
└─────────────────────────────────────────────────────────────────────┘
```

## Deployment Topology Options

```
Option 1: Namespace-Scoped (Recommended)
┌────────────────────────────────────────────────────────────────────┐
│ Namespace: backup-system (Infrastructure)                          │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaRepository: prod-repo                                   │  │
│  │  → Kopia Server Deployment                                   │  │
│  │  → Service + Ingress                                         │  │
│  └──────────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaRepository: staging-repo                                │  │
│  │  → Kopia Server Deployment                                   │  │
│  │  → Service + Ingress                                         │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────┐
│ Namespace: production                                              │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaBackup: db-backup (references: prod-repo)               │  │
│  │  → User: production-db-data                                  │  │
│  │  → CronJob → Connects to prod-repo server                    │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────────┐
│ Namespace: staging                                                 │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaBackup: app-backup (references: staging-repo)           │  │
│  │  → User: staging-app-data                                    │  │
│  │  → CronJob → Connects to staging-repo server                 │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘

Benefits:
✅ Centralized server management
✅ Clear separation of concerns
✅ Easy to manage server resources
✅ Single point for monitoring

Option 2: Co-Located (Alternative)
┌────────────────────────────────────────────────────────────────────┐
│ Namespace: production                                              │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaRepository: prod-repo                                   │  │
│  │  → Kopia Server Deployment (in same namespace)               │  │
│  └──────────────────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ KopiaBackup: db-backup                                       │  │
│  │  → User: production-db-data                                  │  │
│  │  → CronJob → Connects to local server                        │  │
│  └──────────────────────────────────────────────────────────────┘  │
└────────────────────────────────────────────────────────────────────┘

Benefits:
✅ Namespace isolation
✅ No cross-namespace dependencies
❌ More resource usage (one server per namespace)
❌ More complex to manage multiple servers
```

This visual guide should help understand the architecture change and how all components fit together!
