# Upgrade Extended Test Hooks

Hooks allow you to run custom scripts at predefined points in the upgrade flow.
This enables additional validations, customizations, or data collection during tests.

## Available Hook Points

The following hooks are available for the upgrade-extended test:

1. **PRE_SETUP_HOOK**
   - Runs before any setup begins
   - Example: Validate pre-conditions or prepare test environment

2. **POST_SETUP_HOOK**
   - Runs after the environment is set up (cluster, client, CSI, workload)
   - Example: Verify initial setup or collect baseline metrics

3. **PRE_UPGRADE_HOOK**
   - Runs just before starting the upgrade process
   - Example: Perform additional validation before upgrade or backup data

4. **POST_DRIVE_UPGRADE_HOOK**
   - Runs after all drive containers are upgraded
   - Example: Verify drive health or check cluster status

5. **PRE_COMPUTE_UPGRADE_HOOK**
   - Runs before starting compute upgrade phase
   - Example: Check system readiness before proceeding to compute upgrade

6. **POST_COMPUTE_UPGRADE_HOOK**
   - Runs after all compute containers are upgraded
   - Example: Verify compute health or perform computation tests

7. **PRE_CLIENT_UPGRADE_HOOK**
   - Runs before client upgrade begins
   - Example: Check client status or prepare for client upgrade

8. **POST_CLIENT_UPGRADE_HOOK**
   - Runs after client upgrade completes
   - Example: Verify client functionality or run client tests

9. **POST_TEST_HOOK**
   - Runs after the test completes
   - Example: Collect final metrics or generate reports

10. **PRE_CLEANUP_HOOK**
    - Runs before cleanup begins
    - Example: Save logs or perform custom cleanup operations
