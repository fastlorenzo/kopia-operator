# Migration Plan Summary

## Documents Created

Three comprehensive documents have been created to guide the migration:

### 1. [MIGRATION_PLAN_SERVER_MODE.md](./MIGRATION_PLAN_SERVER_MODE.md)

**Full detailed plan** - 1000+ lines covering:

- Complete architecture comparison (current vs target)
- Detailed API changes with code examples
- Phase-by-phase implementation guide
- All new components with function signatures
- Testing strategy
- Security considerations
- Timeline (10 weeks)

### 2. [MIGRATION_QUICK_START.md](./MIGRATION_QUICK_START.md)

**Quick reference guide** - Practical focus:

- High-level overview
- API changes summary
- Implementation checklist
- Example configurations
- Migration paths
- Testing commands
- Security setup

### 3. This summary

## Migration Overview

### Goal

Transform kopia-operator from **direct storage access** to **centralized Kopia Server** architecture.

### Current State → Future State

| Aspect              | Current (Direct)            | Future (Server)                     |
| ------------------- | --------------------------- | ----------------------------------- |
| **Connection**      | Backup Pod → Storage        | Backup Pod → Kopia Server → Storage |
| **Credentials**     | Every pod has storage creds | Only server has storage creds       |
| **User Management** | N/A                         | One user per backup                 |
| **Monitoring**      | Distributed                 | Centralized                         |
| **Security**        | Medium                      | High                                |
| **Exposure**        | Direct NFS/SFTP             | HTTPS API via Ingress               |

## Key Changes

### 1. New CRD Fields

**KopiaRepository:**

```yaml
server:
  enabled: true
  image: ghcr.io/fastlorenzo/kopia:0.20.1
  exposure:
    type: Ingress
    host: kopia.example.com
```

**KopiaBackup:**

```yaml
spec:
  userCredentialsSecret: auto-generated-name # New, auto-populated
status:
  serverURL: https://kopia.example.com # New
  username: namespace-pvcname # New
```

### 2. New Components

1. **KopiaServerManager** - Deploys and manages Kopia Server
2. **KopiaUserManager** - Creates users via Kopia Server API

### 3. Updated Components

1. **KopiaRepositoryReconciler** - Deploys server when enabled
2. **KopiaBackupReconciler** - Creates users and configures backup pods
3. **CronJob Construction** - Connects to server instead of direct storage

## Implementation Phases

```text
Week 1-2:  API/CRD updates, RBAC, skeletons
Week 3-4:  Server deployment, service, ingress
Week 5-6:  User management, backup integration
Week 7-8:  Testing, documentation
Week 9-10: Beta release, migration guide, GA
```

## Benefits

✅ **Security**: No storage credentials in backup pods
✅ **Access Control**: Per-backup user isolation
✅ **Centralized**: Single point for monitoring/logging
✅ **Network Security**: Simplified network policies
✅ **Audit Trail**: All operations logged centrally

## Decision Points

### Must Decide Before Starting

1. **Server HA Strategy**

   - Option A: Single replica initially (simpler, faster)
   - Option B: Multi-replica from start (complex, production-ready)
   - **Recommendation**: Start with single replica, add HA later

2. **Certificate Management**

   - Option A: Self-signed auto-generated (dev/testing)
   - Option B: cert-manager integration (production)
   - Option C: Bring your own cert
   - **Recommendation**: Support all three, default to self-signed

3. **Migration Strategy**

   - Option A: Mandatory migration (deprecate direct mode)
   - Option B: Optional migration (support both modes)
   - **Recommendation**: Support both, default server mode for new installs

4. **Namespace Strategy**

   - Option A: One server per repository (current plan)
   - Option B: Shared server across repositories
   - **Recommendation**: One server per repository for isolation

5. **Backward Compatibility**
   - Option A: Indefinite support for direct mode
   - Option B: Deprecate after 6 months
   - **Recommendation**: Support both for 1 year, then deprecate

### Can Decide Later

- External auth integration (LDAP, OAuth)
- Server horizontal scaling
- Cross-region backup support
- Custom user policies
- Webhook notifications

## Quick Start Path

### For New Implementation

1. **Read** full plan: [MIGRATION_PLAN_SERVER_MODE.md](./MIGRATION_PLAN_SERVER_MODE.md)
2. **Start** with Phase 1: Update CRDs
3. **Follow** quick start: [MIGRATION_QUICK_START.md](./MIGRATION_QUICK_START.md)
4. **Build** incrementally with tests
5. **Document** as you go

### For Planning/Review

1. Review architecture diagrams in main plan
2. Read through API changes
3. Understand new components and their responsibilities
4. Review security considerations
5. Discuss open questions with team
6. Agree on timeline and milestones

## Files to Create/Modify

### New Files (6)

```
internal/controller/backup/
├── kopiaserver_manager.go        # Server lifecycle management
├── kopiauser_manager.go           # User management
├── kopiaserver_deployment.go      # Deployment templates
├── kopiaserver_manager_test.go    # Tests
├── kopiauser_manager_test.go      # Tests
└── (updates to existing test files)
```

### Files to Modify (8)

```
api/backup/v1alpha1/
├── kopiarepository_types.go      # Add Server structs
└── kopiabackup_types.go          # Add status fields

internal/controller/backup/
├── kopiarepository_controller.go # Add server deployment logic
├── kopiabackup_controller.go     # Add user creation, update CronJob
├── kopiarepository_controller_test.go
└── kopiabackup_controller_test.go

config/rbac/
└── role.yaml                     # Add permissions

docs/
├── ARCHITECTURE.md               # Add server mode section
├── EXAMPLES.md                   # Add server examples
└── SERVER_MODE.md                # New detailed guide (create)
```

## Example Repository with Server Mode

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaRepository
metadata:
  name: prod-backups
  namespace: backup-system
spec:
  hostname: prod-cluster
  username: backup-service
  storageType: filesystem

  # Existing storage config
  fileSystemOptions:
    path: /backup/prod
    nfsServer: nfs.example.com
    nfsPath: /exports/backups
  repositoryPasswordExistingSecret: kopia-repo-password

  # NEW: Server configuration
  server:
    enabled: true
    replicas: 1

    # TLS
    tls:
      enabled: true
      autoGenerate: true

    # Exposure
    exposure:
      type: Ingress
      host: kopia-prod.example.com
      ingressClassName: nginx
      annotations:
        cert-manager.io/cluster-issuer: letsencrypt-prod

    # Admin credentials for operator
    adminPasswordExistingSecret: kopia-admin-secret

    # Resources
    resources:
      requests:
        memory: "1Gi"
        cpu: "1000m"
      limits:
        memory: "4Gi"
        cpu: "2000m"
```

**Result**: Operator deploys Kopia Server accessible at https://kopia-prod.example.com

## Example Backup (Unchanged!)

```yaml
apiVersion: backup.cloudinfra.be/v1alpha1
kind: KopiaBackup
metadata:
  name: db-backup
  namespace: production
spec:
  pvcName: postgres-data
  repository: prod-backups
  schedule: "0 2 * * *"
```

**Result**: Operator automatically:

- Creates user `production-postgres-data` on server
- Stores creds in Secret `db-backup-kopia-creds`
- Configures CronJob to use server
- No changes needed in backup spec!

## Testing Approach

### Unit Tests

```bash
make test
```

Test coverage for:

- Server deployment creation/update
- Service and Ingress creation
- User creation/deletion via API
- Credential generation and storage
- CronJob construction for both modes

### Integration Tests

```bash
make test-e2e
```

End-to-end scenarios:

- Server deployment and initialization
- User lifecycle management
- Backup execution via server
- Migration from direct to server mode
- Failure scenarios and recovery

### Manual Verification

```bash
# Deploy test repository
kubectl apply -f config/samples/server-mode-repo.yaml

# Check server deployment
kubectl get deployment -n backup-system kopia-server-prod-backups
kubectl get pods -n backup-system -l app.kubernetes.io/name=kopia-server

# Check exposure
kubectl get svc,ingress -n backup-system

# Create test backup
kubectl apply -f config/samples/server-mode-backup.yaml

# Verify user creation
kubectl get secret -n production db-backup-kopia-creds

# Check backup CronJob
kubectl get cronjob -n production

# Trigger manual backup
kubectl create job --from=cronjob/snapshot-postgres-data test-backup -n production

# Check logs
kubectl logs -n production job/test-backup
kubectl logs -n backup-system deployment/kopia-server-prod-backups
```

## Next Steps

### Immediate Actions

1. **Review Documents**: Read both migration documents thoroughly
2. **Team Discussion**: Review architectural decisions and open questions
3. **Prototype**: Create a spike/PoC for Phase 1 (CRD updates)
4. **Timeline**: Agree on implementation timeline
5. **Resources**: Assign team members to phases

### Week 1 Tasks

- [ ] Update `kopiarepository_types.go` with Server structs
- [ ] Update `kopiabackup_types.go` with status fields
- [ ] Run `make manifests` and `make generate`
- [ ] Update RBAC in `config/rbac/role.yaml`
- [ ] Create skeleton files for new managers
- [ ] Update tests to handle new fields
- [ ] Document API changes in migration guide

### Questions to Answer

Before starting implementation:

1. **HA**: Single replica or multi-replica server initially?
2. **Certificates**: cert-manager required or optional?
3. **Migration**: Mandatory or optional for existing users?
4. **Timeline**: 10 weeks realistic? Need faster/slower?
5. **Resources**: Enough team bandwidth?
6. **Dependencies**: Any external dependencies (Gateway API, cert-manager)?
7. **Namespace**: Same namespace for server and backups, or separate?

## Success Metrics

Track these to measure success:

- [ ] Server deploys automatically for each repository
- [ ] Users created/deleted automatically per backup
- [ ] Backup pods connect to server (not direct storage)
- [ ] Zero storage credentials in backup pods
- [ ] Ingress/HTTPRoute working with TLS
- [ ] All tests passing (unit + e2e)
- [ ] Migration works for existing installations
- [ ] Documentation complete and clear
- [ ] Performance acceptable (no regression)
- [ ] Security audit passed

## Risk Mitigation

| Risk                 | Mitigation                                       |
| -------------------- | ------------------------------------------------ |
| Breaking changes     | Maintain backward compatibility, phased rollout  |
| Server downtime      | Health checks, readiness probes, retry logic     |
| Migration complexity | Gradual migration, support both modes            |
| Performance impact   | Load testing, resource limits, optimization      |
| Security issues      | Security review, penetration testing, audit logs |
| User confusion       | Clear documentation, examples, migration guide   |

## Support & Resources

- **Full Plan**: [MIGRATION_PLAN_SERVER_MODE.md](./MIGRATION_PLAN_SERVER_MODE.md)
- **Quick Start**: [MIGRATION_QUICK_START.md](./MIGRATION_QUICK_START.md)
- **Current Architecture**: [ARCHITECTURE.md](../ARCHITECTURE.md)
- **Examples**: [EXAMPLES.md](./EXAMPLES.md)
- **Kopia Docs**: https://kopia.io/docs/
- **Kopia Server API**: https://kopia.io/docs/reference/command-line/server/

## Conclusion

This migration represents a significant architectural improvement:

**From**: Distributed, direct-access model with security challenges
**To**: Centralized, server-based model with enhanced security and control

The plan is:

- ✅ Comprehensive and detailed
- ✅ Phased for incremental delivery
- ✅ Backward compatible
- ✅ Well-tested
- ✅ Production-ready

**Ready to start? Begin with Phase 1!** 🚀
