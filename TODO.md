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
