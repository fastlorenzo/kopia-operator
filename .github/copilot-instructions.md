# Copilot Instructions for kopia-operator

## Build, Test, and Lint

```bash
# Build
make build                # Compile manager binary (runs generate + manifests + vet first)
make docker-build         # Build container image (IMG=<registry>/<name>:<tag>)

# Test
make test                 # Unit tests via envtest (excludes e2e)
make test-e2e             # E2E tests (requires kind cluster with operator deployed)

# Run a single test or focused suite
go test ./internal/controller/backup/ -run TestControllers -v          # by Go test name
go test ./internal/controller/backup/ -v -ginkgo.focus="CronJob"      # by Ginkgo description

# Lint
make lint                 # golangci-lint
make lint-fix             # golangci-lint with auto-fix

# Code generation (run after changing types or RBAC markers)
make manifests            # Regenerate CRDs, RBAC, webhook configs
make generate             # Regenerate DeepCopy methods

# Local development
make run                  # Run operator locally against current kubeconfig
make install              # Install CRDs into cluster
make deploy IMG=<image>   # Full deploy (CRDs + RBAC + operator Deployment)
```

## Architecture

Kubernetes operator for automating PVC backups using [Kopia](https://kopia.io/). Built with Kubebuilder v4 and controller-runtime v0.20.4.

**Two CRDs** in API group `backup.cloudinfra.be/v1alpha1`:

- **KopiaRepository** — defines storage backend (filesystem/NFS or SFTP), credentials, caching, and optional Kopia Server mode
- **KopiaBackup** — references a PVC and a KopiaRepository, produces a CronJob that runs `kopia snapshot create`

**Dual operating modes** determined by `KopiaRepository.Spec.Server.Enabled`:

| | Direct Mode | Server Mode |
|---|---|---|
| Storage access | Each backup pod connects directly | Only the Kopia Server pod connects |
| Credentials | Repository password mounted to every CronJob | Per-backup user credentials via HTTPS API |
| Resources | CronJob + ConfigMap | Deployment + Service + TLS Secret + per-user Secrets + CronJob |

**Controller → Manager delegation**: Both reconcilers share singleton `KopiaServerManager` and `KopiaUserManager` instances (created in `cmd/main.go`). Server manager handles Deployment/Service/TLS lifecycle. User manager handles per-backup user CRUD via `kubectl exec` into the server pod.

**Auto-creation flow**: Adding label `backup.cloudinfra.be/repository: <name>` to a PVC triggers automatic KopiaBackup creation using the repository's `defaultSchedule`. The PVC becomes the owner (cascade delete).

**Cross-namespace repository lookup**: `getKopiaRepositoryByName()` first checks the backup's namespace, then searches all namespaces. Errors if ambiguous (multiple repos with same name).

## Key Conventions

### Status Conditions

Always use `meta.SetStatusCondition` with constants from `kopiabackup_types.go` / `kopiarepository_types.go`:

```go
meta.SetStatusCondition(&backup.Status.Conditions, metav1.Condition{
    Type:               backupv1alpha1.ConditionTypeReady,
    Status:             metav1.ConditionTrue,
    Reason:             backupv1alpha1.ReasonReconciled,
    Message:            "CronJob created successfully",
    ObservedGeneration: backup.Generation,
})
```

Always follow with `r.Status().Update(ctx, &resource)`. Record a Kubernetes event alongside condition changes via `r.Recorder.Event()`.

### Error Handling in Reconcile

- **Validation failures** (PVC not found, repo not found): set condition to `False`, update status, return `ctrl.Result{RequeueAfter: requeueDelay}` with `nil` error
- **Transient failures** (server not ready): type-check with `errors.As()` for `ServerNotReadyError`, requeue with delay
- **Infrastructure errors** (API failures): return the error directly to let controller-runtime handle exponential backoff
- Wrap all errors with context: `fmt.Errorf("creating cronjob: %w", err)`

### Resource Naming

| Resource | Pattern | Example |
|----------|---------|---------|
| CronJob | `snapshot-<pvc-name>` (truncated to 63 chars) | `snapshot-postgres-data` |
| Server Deployment | `kopia-server-<repo-name>` | `kopia-server-prod-repo` |
| Server Service | `kopia-server-<repo-name>` | `kopia-server-prod-repo` |
| TLS Secret | `kopia-server-tls-<repo-name>` | `kopia-server-tls-prod-repo` |
| User Secret | `kopia-server-user-<ns>-<pvc>-<repo>` | `kopia-server-user-default-pgdata-prod-repo` |
| ConfigMap | `kopia-repo-config-<repo-name>` | `kopia-repo-config-prod-repo` |

Long PVC names use `snapshot-<first-42-chars>-<last-char>` to stay within DNS label limits.

### Labels and Annotations

Domain prefix: `backup.cloudinfra.be/`

```yaml
# Set on CronJob/Pod
backup.cloudinfra.be/backup: <backup-name>
backup.cloudinfra.be/repository: <repo-name>
backup.cloudinfra.be/pvc-name: <pvc-name>
backup.cloudinfra.be/node-name: <node>
app.kubernetes.io/name: <app>          # from source pod
sidecar.istio.io/inject: "false"       # disable Istio sidecar

# Set on PVC to trigger auto-creation
backup.cloudinfra.be/repository: <repo-name>

# Optional annotation on PVC
backup.cloudinfra.be/schedule: "0 3 * * *"   # override default schedule
```

### Secrets

- Repository password secret must have key `KOPIA_PASSWORD` (not `password`)
- No plaintext password fields exist in the API — only `passwordSecretName` and `adminPasswordSecretName` referencing Secrets

### RBAC

Defined via `+kubebuilder:rbac` markers on the Reconcile methods. After changing markers, run `make manifests` to regenerate `config/rbac/role.yaml`. The operator needs `pods/exec` permission for server mode user management.

### Testing

- **Unit tests**: Ginkgo v2 + Gomega + envtest. Suite setup in `suite_test.go` boots a real API server with CRDs from `config/crd/bases/`.
- **E2E tests**: Ginkgo `Ordered` suite in `test/e2e/`. Builds operator image, loads into kind, deploys via `make deploy`, creates test resources (SFTP server, PVCs, CRDs).
- **Envtest K8s version**: 1.32.0 (set in Makefile as `ENVTEST_K8S_VERSION`)

### Kopia Image

Default pinned to `ghcr.io/fastlorenzo/kopia:0.20.1@sha256:...` in `kopiabackup_controller.go` (`DefaultKopiaImage` constant). Overridable via `--kopia-image` flag or `KopiaRepository.Spec.Server.Image`.
