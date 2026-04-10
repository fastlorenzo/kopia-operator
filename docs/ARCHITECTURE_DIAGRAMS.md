# Kopia Operator Server Mode - Visual Architecture

## Current Architecture (Direct Storage)

```mermaid
graph TD
    subgraph cluster["Kubernetes Cluster"]
        subgraph ns["Namespace: default"]
            subgraph appPod["Application Pod"]
                App["App<br/>Writes data to PVC"]
            end
            App --> PVC1["PVC (RWO)"]

            subgraph cronJob["Backup CronJob"]
                Kopia["Kopia Client<br/>Has storage credentials"]
            end
            PVC1 -.->|ReadOnly| Kopia
        end

        CM["ConfigMap / Secrets<br/>- NFS Server: nfs.example.com<br/>- NFS Path: /exports/backups<br/>- Repository Password<br/>- SFTP Keys (if SFTP)"]
        Kopia -->|Direct Connection| CM
    end

    CM -->|"Direct Access<br/>(Every backup pod has creds)"| Storage

    subgraph Storage["External Storage (Outside K8s)"]
        NFS["NFS Server / SFTP Server<br/>/exports/backups/<br/>├── kopia.repository.f<br/>├── snapshots/<br/>└── ..."]
    end

    style cluster fill:#fff,stroke:#333
    style Storage fill:#f5f5f5,stroke:#333
```

> **Issues with Current Architecture:**
> - ❌ Every backup pod needs storage credentials (security risk)
> - ❌ Storage directly exposed to all backup pods (network complexity)
> - ❌ No centralized access control or user management
> - ❌ Difficult to audit who accessed what
> - ❌ Network policies must allow all backup pods to access storage
> - ❌ Credential rotation affects all backup pods

## Target Architecture (Kopia Server Mode)

```mermaid
graph TD
    subgraph cluster["Kubernetes Cluster"]
        subgraph appNS["Namespace: default (Application Namespace)"]
            subgraph appPod["Application Pod"]
                App["App<br/>Writes data to PVC"]
            end
            App --> PVC1["PVC (RWO)"]

            subgraph cronJob["Backup CronJob"]
                KopiaClient["Kopia Client<br/>Only has user creds"]
            end
            PVC1 -.->|ReadOnly| KopiaClient

            UserSecret["Secret: default-myapp-kopia-creds<br/>- username: default-myapp<br/>- password: ***************"]
            KopiaClient --> UserSecret
        end

        UserSecret -->|"HTTPS API<br/>(User credentials)"| Svc

        subgraph backupNS["Namespace: backup-system (Centralized Backup Infrastructure)"]
            subgraph serverDeploy["Kopia Server Deployment"]
                subgraph serverPod["Pod: kopia-server-prod-repo-xxxxx"]
                    UserMgmt["**User Management**<br/>- default-myapp (password)<br/>- production-db (password)<br/>- staging-cache (password)"]
                    API["**API Server** (HTTPS :51515)<br/>- /api/v1/snapshot<br/>- /api/v1/repo/status<br/>- /api/v1/users"]
                    StorageConn["**Storage Backend Connection**<br/>- Has storage credentials<br/>- Manages repository"]
                end
                Volumes["Volume Mount<br/>- NFS Mount (or)<br/>- SFTP Config"]
                StorageConn --> Volumes
            end

            Svc["Service<br/>ClusterIP :51515"]
            Svc --> serverPod

            Ingress["Ingress / HTTPRoute<br/>Host: kopia-prod.example.com<br/>TLS: letsencrypt-prod"]
            Ingress --> Svc
        end
    end

    Ingress -->|"HTTPS<br/>(Only server has storage creds)"| ExtStorage

    subgraph ExtStorage["External Storage (Outside K8s)"]
        NFS["NFS Server / SFTP Server<br/>/exports/backups/<br/>├── kopia.repository.f<br/>├── snapshots/<br/>│   └── default-myapp/<br/>│   └── production-db/<br/>└── ..."]
    end

    style cluster fill:#fff,stroke:#333
    style ExtStorage fill:#f5f5f5,stroke:#333
```

> **Benefits of New Architecture:**
> - ✅ Only Kopia Server has storage credentials (1 place vs N places)
> - ✅ Per-backup user authentication and authorization
> - ✅ Centralized audit logging and monitoring
> - ✅ Simplified network policies (only server → storage)
> - ✅ Easy credential rotation (only update server)
> - ✅ HTTPS API with TLS encryption
> - ✅ Ingress integration for external access
> - ✅ Better compliance and security posture

## Component Interaction Flow

```mermaid
flowchart TD
    %% 1. Repository Creation
    R1["**1. Repository Creation**<br/>User creates KopiaRepository with server.enabled: true"]
    R2["**KopiaRepositoryReconciler**<br/>1. Validates repository spec<br/>2. Creates admin secret (if not exists)<br/>3. Calls KopiaServerManager.EnsureServerDeployment()<br/>4. Calls KopiaServerManager.EnsureServerService()<br/>5. Calls KopiaServerManager.EnsureServerExposure()<br/>6. Waits for server readiness<br/>7. Initializes repository on server<br/>8. Updates status.serverURL"]
    R3["**Kubernetes Resources Created:**<br/>✅ Deployment: kopia-server-repo-name<br/>✅ Service: kopia-server-repo-name<br/>✅ Ingress/HTTPRoute: kopia-server-repo-name<br/>✅ ConfigMap: kopia-config-repo-name (optional)<br/>✅ Secret: repo-name-tls (if auto-generated)"]

    R1 --> R2 --> R3

    %% 2. Backup Creation
    B1["**2. Backup Creation**<br/>User creates KopiaBackup"]
    B2["**KopiaBackupReconciler**<br/>1. Validates backup spec<br/>2. Fetches referenced KopiaRepository<br/>3. Waits for repository.status.serverReady == true<br/>4. Calls KopiaUserManager.EnsureUser()<br/>&nbsp;&nbsp;&nbsp;→ Creates user on Kopia Server via API<br/>&nbsp;&nbsp;&nbsp;→ Generates secure password<br/>&nbsp;&nbsp;&nbsp;→ Stores credentials in Secret<br/>5. Discovers runtime info (node, app, pod)<br/>6. Creates/Updates CronJob with server connection<br/>7. Updates backup status"]
    B3["**Kubernetes Resources Created:**<br/>✅ Secret: backup-name-kopia-creds<br/>&nbsp;&nbsp;- username: namespace-pvcname<br/>&nbsp;&nbsp;- password: generated-secure-password<br/>✅ CronJob: snapshot-pvcname<br/>&nbsp;&nbsp;- Env: KOPIA_SERVER_URL, USERNAME, PASSWORD<br/>&nbsp;&nbsp;- Command: kopia server login && kopia snapshot create"]

    R3 --> B1 --> B2 --> B3

    %% 3. Backup Execution
    E1["**3. Backup Execution**<br/>CronJob triggers on schedule"]
    E2["**Backup Pod Execution Flow**<br/>1. Init Container: Random wait (1-900s)<br/>2. Main Container:<br/>&nbsp;&nbsp;a. Read credentials from Secret<br/>&nbsp;&nbsp;b. Connect: kopia server login<br/>&nbsp;&nbsp;c. Snapshot: kopia snapshot create /data/...<br/>&nbsp;&nbsp;d. List: kopia snapshot list /data/...<br/>&nbsp;&nbsp;e. Stats: kopia content stats<br/>&nbsp;&nbsp;f. Disconnect: kopia server logout<br/>3. Pod completes successfully"]
    E3["**Kopia Server Processes Request:**<br/>1. Authenticates user credentials<br/>2. Authorizes access to snapshot path<br/>3. Receives backup data stream<br/>4. Deduplicates and compresses data<br/>5. Writes to backend storage (NFS/SFTP)<br/>6. Logs operation with user context<br/>7. Returns success/failure to client"]

    B3 --> E1 --> E2 --> E3

    %% 4. Backup Deletion
    D1["**4. Backup Deletion**<br/>User deletes KopiaBackup"]
    D2["**KopiaBackupReconciler Finalizer**<br/>1. Calls KopiaUserManager.DeleteUser()<br/>&nbsp;&nbsp;&nbsp;→ Deletes user from Kopia Server via API<br/>2. Deletes credentials Secret<br/>3. CronJob auto-deleted (owner reference)<br/>4. Removes finalizer"]

    E3 ~~~ D1 --> D2

    %% 5. Repository Deletion
    RD1["**5. Repository Deletion**<br/>User deletes KopiaRepository"]
    RD2["**KopiaRepositoryReconciler Finalizer**<br/>1. Verifies no dependent KopiaBackups exist<br/>2. Deletes Ingress/HTTPRoute<br/>3. Deletes Service<br/>4. Deletes Deployment (Server pods terminated)<br/>5. Removes finalizer"]

    D2 ~~~ RD1 --> RD2
```

## Security Architecture

```mermaid
graph TD
    subgraph L1["Layer 1: Network Security"]
        BackupPods["Backup Pods"] <-->|HTTPS/TLS| KopiaServer["Kopia Server"]
        KopiaServer -->|"Only Server<br/>has access"| StorageBackend["Storage"]
    end

    subgraph L2["Layer 2: Authentication & Authorization"]
        UserMgmt["**User Management** (on Kopia Server)<br/>- Each backup has unique username/password<br/>- Passwords auto-generated (32+ chars)<br/>- Stored in Kubernetes Secrets<br/>- Scoped to backup namespace"]
        AccessCtrl["**Access Control**<br/>- Users can only access their own snapshots<br/>- Path-based authorization<br/>- Admin user (operator only) for user management"]
    end

    subgraph L3["Layer 3: Credential Management"]
        AdminCreds["**Admin Credentials** (Operator → Server)<br/>Secret: kopia-admin-secret<br/>- username: admin<br/>- password: strong-generated-password<br/>- Used only by operator for user management"]
        StorageCreds["**Storage Credentials** (Server → Backend)<br/>Secret: kopia-repo-password<br/>- KOPIA_PASSWORD: repository-password<br/>- Mounted only to server pods<br/>- Not accessible to backup pods"]
        UserCreds["**User Credentials** (Backup Pod → Server)<br/>Secret: backup-name-kopia-creds<br/>- username: namespace-pvcname<br/>- password: auto-generated-secure-password<br/>- Scoped to backup namespace"]
    end

    subgraph L4["Layer 4: Audit & Logging"]
        ServerLogs["**Server Audit Logs:**<br/>✅ User authentication events<br/>✅ Snapshot creation/deletion<br/>✅ Failed login attempts<br/>✅ API access logs<br/>✅ User management operations"]
        OperatorLogs["**Operator Audit Logs:**<br/>✅ Repository creation/deletion<br/>✅ Server deployment events<br/>✅ User creation/deletion<br/>✅ Configuration changes"]
    end

    L1 ~~~ L2 ~~~ L3 ~~~ L4
```

> **Network Policies:**
> - Backup pods → Server only
> - Server → Storage only
> - No direct backup pod → storage

## Deployment Topology Options

### Option 1: Namespace-Scoped (Recommended)

```mermaid
graph TD
    subgraph backupSystem["Namespace: backup-system (Infrastructure)"]
        ProdRepo["**KopiaRepository: prod-repo**<br/>→ Kopia Server Deployment<br/>→ Service + Ingress"]
        StagingRepo["**KopiaRepository: staging-repo**<br/>→ Kopia Server Deployment<br/>→ Service + Ingress"]
    end

    subgraph production["Namespace: production"]
        ProdBackup["**KopiaBackup: db-backup** (references: prod-repo)<br/>→ User: production-db-data<br/>→ CronJob → Connects to prod-repo server"]
    end

    subgraph staging["Namespace: staging"]
        StagingBackup["**KopiaBackup: app-backup** (references: staging-repo)<br/>→ User: staging-app-data<br/>→ CronJob → Connects to staging-repo server"]
    end

    ProdBackup -->|HTTPS| ProdRepo
    StagingBackup -->|HTTPS| StagingRepo
```

> ✅ Centralized server management · ✅ Clear separation of concerns · ✅ Easy to manage server resources · ✅ Single point for monitoring

### Option 2: Co-Located (Alternative)

```mermaid
graph TD
    subgraph production["Namespace: production"]
        ProdRepo2["**KopiaRepository: prod-repo**<br/>→ Kopia Server Deployment (in same namespace)"]
        ProdBackup2["**KopiaBackup: db-backup**<br/>→ User: production-db-data<br/>→ CronJob → Connects to local server"]
        ProdBackup2 -->|HTTPS| ProdRepo2
    end
```

> ✅ Namespace isolation · ✅ No cross-namespace dependencies · ❌ More resource usage (one server per namespace) · ❌ More complex to manage multiple servers

This visual guide should help understand the architecture change and how all components fit together!
