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

The security model is built around four layers, each enforced by different Kubernetes and Kopia mechanisms.

### Network & Credential Flow

```mermaid
graph LR
    subgraph appNS["Application Namespace"]
        CronJob["Backup CronJob Pod"]
        UserSecret["🔑 Secret<br/>‹backup›-kopia-creds"]
    end

    subgraph operatorNS["Operator Namespace"]
        Operator["Kopia Operator"]
        AdminSecret["🔑 Secret<br/>kopia-admin-secret"]
    end

    subgraph serverNS["Server Namespace"]
        Server["Kopia Server"]
        RepoSecret["🔑 Secret<br/>kopia-repo-password"]
    end

    Storage[("External Storage<br/>NFS / SFTP")]

    CronJob -->|"reads"| UserSecret
    CronJob ==>|"HTTPS :51515<br/>user credentials"| Server

    Operator -->|"reads"| AdminSecret
    Operator ==>|"kubectl exec<br/>admin credentials"| Server

    Server -->|"reads"| RepoSecret
    Server ==>|"NFS/SFTP<br/>storage credentials"| Storage

    CronJob -.-x|"❌ blocked"| Storage

    style Storage fill:#f9f9f9,stroke:#999
    linkStyle 6 stroke:red,stroke-dasharray:5
```

### Layer Details

| Layer | Mechanism | What it protects |
|-------|-----------|-----------------|
| **Network** | Only the Kopia Server connects to storage. Backup pods only talk to the server over HTTPS/TLS. Network policies block direct pod→storage access. | Storage backend isolation |
| **Authentication** | Each KopiaBackup gets a unique username + auto-generated 32+ char password, stored in a namespaced Secret. Server validates credentials on every API call. | Per-backup identity |
| **Authorization** | Users can only access snapshots under their own path (`/data/‹ns›/‹app›/‹pvc›`). Admin user (operator-only) is the sole account that can manage users. | Snapshot isolation |
| **Audit** | Server logs all auth events, snapshot operations, and failed login attempts. Operator logs resource lifecycle events (create/delete users, repos, servers). | Observability & compliance |

### Credential Scoping

```mermaid
graph TD
    subgraph secrets["Three separate credential domains"]
        direction LR
        A["🟢 **User Credentials**<br/>Per-backup · app namespace<br/>Backup pod → Server"]
        B["🟡 **Admin Credentials**<br/>Per-repository · operator only<br/>Operator → Server"]
        C["🔴 **Storage Credentials**<br/>Per-repository · server only<br/>Server → NFS/SFTP"]
    end

    A -.->|"scope: single backup"| scope1["CronJob Pod"]
    B -.->|"scope: operator process"| scope2["Operator Pod"]
    C -.->|"scope: server pod"| scope3["Kopia Server Pod"]
```

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
