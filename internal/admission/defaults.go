package admission

// Per-CRD defaults: policy ID → baked-in (strict, relaxed) Mode.
// ValidateRegistry() enforces validator↔row bijection at startup.
var (
	wekaClusterDefaults = map[string]PolicyDefaults{
		"cluster_min_drives_feasibility":      {Strict: Error, Relaxed: Error},
		"cluster_min_drives_large_floor":      {Strict: Warn, Relaxed: Warn},
		"cluster_min_drives_small_advisory":   {Strict: Warn, Relaxed: Warn},
		"cluster_selected_nodes_count":        {Strict: Error, Relaxed: Warn},
		"cluster_drivers_dist_service_exists": {Strict: Error, Relaxed: Warn},
		"cluster_cores_available":             {Strict: Warn, Relaxed: Warn},
		"cluster_hugepages_available":         {Strict: Warn, Relaxed: Warn},
		"cluster_signed_drives":               {Strict: Error, Relaxed: Warn},
		"cluster_drives_unsigned_advisory":    {Strict: Warn, Relaxed: Warn},
		"cluster_network_ethdevice":           {Strict: Warn, Relaxed: Warn},
		"cluster_drive_compute_core_ratio":    {Strict: Warn, Relaxed: Warn},
		"cluster_compute_drive_cores_floor":   {Strict: Error, Relaxed: Warn},
		"cluster_drive_cores_below_capacity":  {Strict: Warn, Relaxed: Warn},
		// Stricter than cluster_drive_cores_below_capacity, which warns about a shortfall the operator can
		// be told to fix by raising driveCores: here no legal driveCores exists, so the configured capacity
		// is unreachable however the cluster is edited afterwards. Relaxed still warns — the containers do
		// run, they just never finish adding drives.
		"cluster_num_drives_below_required_cores": {Strict: Error, Relaxed: Warn},
		"cluster_cores_per_container_limit":       {Strict: Error, Relaxed: Warn},
		// Both auto-full-drives policies describe a cluster that never converges, so strict rejects;
		// relaxed warns so a fleet can still be applied and inspected. cluster_auto_full_drives_feasible
		// asks the planner directly and rejects a plan it calls infeasible — nothing gets created at all.
		// min_nodes covers the one case the planner cannot see, a drive selector below the form-cluster
		// floor: that plan IS feasible and the containers run, the cluster just never forms.
		"cluster_auto_full_drives_feasible":  {Strict: Error, Relaxed: Warn},
		"cluster_auto_full_drives_min_nodes": {Strict: Error, Relaxed: Warn},
		// Error in BOTH modes: below the form-cluster minimum the cluster does not degrade, it never forms
		// at all (waits forever on MinContainersNotReady), so relaxing this would only delay the failure.
		"cluster_min_containers":        {Strict: Error, Relaxed: Error},
		"cluster_dataservices_fe_cores": {Strict: Error, Relaxed: Error},
		"cluster_capacity_protection":   {Strict: Error, Relaxed: Error},
		// Warn in both modes: unlike cluster_min_containers, which sees a definite pinned count below the
		// minimum, a low protection floor only PERMITS an undersized derived plan — it does not prove one,
		// since a large capacity target can still derive plenty of containers.
		"cluster_capacity_min_drive_containers": {Strict: Warn, Relaxed: Warn},
		"cluster_capacity_chunk_feasibility":    {Strict: Error, Relaxed: Error},
		"cluster_skip_default_fs":               {Strict: Warn, Relaxed: Warn},
		"cluster_podspec_syntax":                {Strict: Error, Relaxed: Error},
		"cluster_extra_volumes":                 {Strict: Error, Relaxed: Error},
	}

	wekaClientDefaults = map[string]PolicyDefaults{
		"client_target_cluster_exists":        {Strict: Error, Relaxed: Warn},
		"client_podspec_syntax":               {Strict: Error, Relaxed: Error},
		"client_wekahome_cacert_unverifiable": {Strict: Warn, Relaxed: Warn},
		"client_extra_volumes":                {Strict: Error, Relaxed: Error},
	}

	// Update-only defaults: cores-decrease checks are always Error regardless
	// of strict/relaxed mode — decreasing cores is never a safe operation.
	wekaClusterUpdateDefaults = map[string]PolicyDefaults{
		"cluster_cores_decrease": {Strict: Error, Relaxed: Error},
		// Error in BOTH modes: flipping the derived sizing mode under a live cluster has no
		// degraded-but-working outcome — two sizing regimes would fight over the same drives. The two
		// switches the operator can actually carry over are allowlisted in the validator itself.
		"cluster_sizing_mode_flip": {Strict: Error, Relaxed: Error},
	}
	wekaContainerUpdateDefaults = map[string]PolicyDefaults{
		"container_cores_decrease": {Strict: Error, Relaxed: Error},
	}
	wekaClientUpdateDefaults = map[string]PolicyDefaults{
		"client_cores_decrease": {Strict: Error, Relaxed: Error},
	}
)
