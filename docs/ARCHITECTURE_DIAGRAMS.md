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

### 1. Repository Lifecycle

```mermaid
sequenceDiagram
    actor User
    participant API as Kubernetes API
    participant RR as KopiaRepository<br/>Reconciler
    participant SM as KopiaServer<br/>Manager
    participant K8s as Kubernetes<br/>Resources

    User->>API: Create KopiaRepository<br/>(server.enabled: true)
    API-->>RR: Reconcile event

    RR->>API: Validate repository spec
    RR->>API: Create admin Secret<br/>(if not exists)

    RR->>SM: EnsureServerDeployment()
    SM->>K8s: Create/Update Deployment<br/>kopia-server-‹repo›
    SM->>K8s: Create/Update ConfigMap<br/>kopia-config-‹repo›
    SM->>K8s: Generate TLS cert → Secret<br/>‹repo›-tls

    RR->>SM: EnsureServerService()
    SM->>K8s: Create/Update Service<br/>ClusterIP :51515

    RR->>SM: EnsureServerExposure()
    SM->>K8s: Create Ingress/HTTPRoute<br/>(if configured)

    loop Wait for readiness
        RR->>API: Check Deployment status
        API-->>RR: Pods ready / not ready
    end

    RR->>API: Update status.serverURL<br/>Update status.serverReady = true
    RR->>API: Set Condition: Ready=True
```

### 2. Backup Lifecycle

```mermaid
sequenceDiagram
    actor User
    participant API as Kubernetes API
    participant BR as KopiaBackup<br/>Reconciler
    participant UM as KopiaUser<br/>Manager
    participant Server as Kopia Server<br/>Pod
    participant K8s as Kubernetes<br/>Resources

    User->>API: Create KopiaBackup<br/>(pvcName, schedule, repository)
    API-->>BR: Reconcile event

    BR->>API: Fetch KopiaRepository
    BR->>API: Check repository.status.serverReady

    rect rgb(240, 248, 255)
        Note over BR,Server: User provisioning (server mode)
        BR->>UM: EnsureUser(backup)
        UM->>Server: kubectl exec:<br/>kopia server user add<br/>‹namespace›-‹pvcname›
        Server-->>UM: User created
        UM->>K8s: Create Secret<br/>‹backup›-kopia-creds<br/>(username + generated password)
    end

    BR->>API: List Pods mounting PVC
    API-->>BR: Pod found → extract node, app name

    BR->>K8s: Create/Update CronJob<br/>snapshot-‹pvcname›<br/>(nodeAffinity, server env vars)
    BR->>K8s: Create/Update ConfigMap<br/>(direct mode only)

    BR->>API: Set owner reference (Backup → CronJob)
    BR->>API: Update status + Condition: Ready=True

    rect rgb(255, 240, 240)
        Note over User,K8s: Deletion flow (finalizer)
        User->>API: Delete KopiaBackup
        API-->>BR: Reconcile (deletionTimestamp set)
        BR->>UM: DeleteUser(backup)
        UM->>Server: kubectl exec:<br/>kopia server user delete
        BR->>K8s: Delete credentials Secret
        Note over K8s: CronJob auto-deleted<br/>via owner reference
        BR->>API: Remove finalizer
    end
```

### 3. Backup Execution

```mermaid
sequenceDiagram
    participant Cron as CronJob<br/>Scheduler
    participant Init as Init Container<br/>(wait)
    participant Snap as Snapshot Container<br/>(kopia client)
    participant Server as Kopia Server
    participant Storage as External Storage<br/>(NFS / SFTP)

    Cron->>Init: Trigger scheduled job
    Note over Init: sleep random(1-900s)<br/>Prevents backup storms

    Init->>Snap: Init complete, start main container

    Snap->>Snap: Read credentials from<br/>mounted Secret

    Snap->>Server: kopia server login<br/>--url=$KOPIA_SERVER_URL<br/>--username=$KOPIA_SERVER_USERNAME<br/>--password=$KOPIA_SERVER_PASSWORD
    Server-->>Snap: Authenticated ✓

    Snap->>Server: kopia snapshot create /data/‹ns›/‹app›/‹pvc›
    activate Server
    Server->>Server: Authenticate & authorize user
    Server->>Server: Deduplicate & compress
    Server->>Storage: Write snapshot data
    Storage-->>Server: Write confirmed
    Server-->>Snap: Snapshot created ✓
    deactivate Server

    Snap->>Server: kopia snapshot list /data/...
    Server-->>Snap: Snapshot listing

    Snap->>Server: kopia content stats
    Server-->>Snap: Content statistics

    Snap->>Server: kopia server logout
    Server-->>Snap: Disconnected

    Note over Snap: Pod completes successfully
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
