# TODO

## Tasks

### Fix CronJob Creation for Orphan PVCs

**Issue**: The current implementation doesn't properly handle the creation of cronjobs for orphan PVCs (PVCs that are not linked to any pod).

**Details**:

- When a PVC exists without being attached to a pod, the cronjob creation process fails or doesn't work as expected
- Need to investigate why orphan PVCs are not being handled correctly
- The controller should be able to create backup cronjobs for standalone PVCs

**Tasks to complete**:

- [ ] Investigate the root cause of why orphan PVC detection/handling fails
- [ ] Identify where in the controller logic the PVC-to-Pod linkage is required
- [ ] Implement proper handling for PVCs without pod attachments
- [ ] Add validation to check if a PVC is orphaned (no pod references)
- [ ] Update the cronjob creation logic to work with orphan PVCs
- [ ] Add tests for orphan PVC scenarios
- [ ] Document the expected behavior for orphan PVCs

**Priority**: Medium

**Related Files**:

- Controller logic for PVC backup handling
- CronJob creation code

---

### Add E2E Test with S3 Backend

**Description**: Add end-to-end tests using S3 as the storage backend for the repository server.

**Tasks to complete**:

- [ ] Set up S3 (or S3-compatible like MinIO) test infrastructure
- [ ] Create test configuration for S3 backend repository
- [ ] Implement E2E test cases for backup/restore with S3 backend
- [ ] Add CI pipeline support for S3 backend tests

**Priority**: Medium

---

### Add E2E Test for Direct Connection (Legacy Setup)

**Description**: Add end-to-end tests for direct connection to the storage backend without going through the repository server. This is needed to support legacy setups.

**Tasks to complete**:

- [ ] Create test configuration for direct backend connection
- [ ] Implement E2E test cases for direct SFTP/S3 connection
- [ ] Test backup/restore operations without repository server
- [ ] Ensure backward compatibility with legacy configurations
- [ ] Document the differences between repository server and direct connection modes

**Priority**: Medium

---

### Refactor E2E Tests for Better Readability

**Description**: Refactor the end-to-end tests by splitting them into different files for better readability and maintainability.

**Tasks to complete**:

- [ ] Analyze current e2e_test.go structure and identify logical groupings
- [ ] Split tests by feature/functionality (e.g., repository, backup, restore, cronjob)
- [ ] Create separate test files for different backend types (SFTP, S3)
- [ ] Extract common test utilities and helpers into shared files
- [ ] Ensure proper test suite organization with Ginkgo
- [ ] Update test documentation

**Priority**: Low

**Related Files**:

- test/e2e/e2e_test.go
- test/e2e/e2e_suite_test.go
- test/utils/utils.go

**Priority**: Medium

- add possibility to set retention policies on each backup