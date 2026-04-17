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
		"cluster_network_ethdevice":           {Strict: Warn, Relaxed: Warn},
		"cluster_drive_compute_core_ratio":    {Strict: Warn, Relaxed: Warn},
	}

	wekaClientDefaults = map[string]PolicyDefaults{
		"client_target_cluster_exists": {Strict: Error, Relaxed: Warn},
	}
)
