# Weka CLI Reference

This document provides reference information for the Weka command-line interface (CLI) commands.

## weka status Command

The `weka status` command provides comprehensive information about the current state of a Weka cluster.

### Using JSON Output Format

Adding the `--json` flag to the `weka status` command returns the output in JSON format, which is useful for programmatic processing of the status information.

### Example Output

Below is a complete example of the `weka status --json` command output with all fields that a demo cluster returns. This can be used as a reference for what information is available through the command.

```json
{
    "active_alerts_count": 34,
    "activity": {
        "num_ops": 0,
        "num_reads": 0,
        "num_writes": 0,
        "obs_download_bytes_per_second": 0,
        "obs_upload_bytes_per_second": 0,
        "sum_bytes_read": 0,
        "sum_bytes_written": 0
    },
    "block_upgrade_task": {
        "progress": 0,
        "state": "IDLE",
        "taskId": "BlockTaskId<0>",
        "type": "INVALID"
    },
    "bucket_failure_grace_msecs": 14000,
    "buckets": {
        "active": 156,
        "flush_finished": 0,
        "global_flush_generation": 1,
        "global_flush_status": "NONE",
        "included_in_report": 156,
        "shutdown_finished": 0,
        "total": 156
    },
    "buckets_info": {
        "averageFillLevelPPM": 219976,
        "capacityDiscrepancyGracePPM": 50000,
        "criticalRaidFillLevelPPM": 970000,
        "hysteresisPPM": 20000,
        "maxPrefetchRPCs": 256,
        "maxProvisionablePPM": 920000,
        "placementAllocationThresholdPPM": 450000,
        "reportedRaidFillLevelSeverity": "Normal",
        "shrunkAtGeneration": "ConfigGeneration<INVALID>",
        "ssdsFillLevelPPM": 24560,
        "ssdsTrueFillLevelPPM": 24560,
        "thinProvisionState": {
            "shrinkageFactor": {
                "val": 4096
            },
            "totalSSDBudgets": 4522794373,
            "usableWritable": 43030550937
        }
    },
    "build_options": {
        "asserts_enabled": false,
        "fully_optimized_build": true,
        "release_enabled": true
    },
    "capacity": {
        "hot_spare_bytes": 0,
        "total_bytes": 176253136637952,
        "unavailable_bytes": 0,
        "unprovisioned_bytes": 174497352261632
    },
    "cloud": {
        "enabled": true,
        "healthy": true,
        "proxy": "",
        "url": ""
    },
    "compute_upgrade": {
        "source_version_sig": "0xffffffffffffffff"
    },
    "containers": {
        "active": 16,
        "backends": {
            "active": 12,
            "total": 13
        },
        "clients": {
            "active": 4,
            "total": 4
        },
        "computes": {
            "active": 6,
            "total": 6
        },
        "drives": {
            "active": 6,
            "total": 7
        },
        "total": 17
    },
    "drives": {
        "active": 36,
        "total": 36
    },
    "failure_domains_enabled": false,
    "grim_reaper": {
        "enabled": true,
        "is_cluster_fully_connected": true,
        "node_with_least_links": null
    },
    "guid": "550c68b6-8f64-4073-8086-efc3ba69207e",
    "hanging_ios": {
        "alerts_threshold_secs": 900,
        "event_backend_threshold_secs": 1800,
        "event_driver_frontend_threshold_secs": 1800,
        "event_nfs_frontend_threshold_secs": 1800,
        "last_emitted_backend_event": "2025-03-05T03:38:46.375379Z",
        "last_emitted_backend_no_longer_detected_event": "2025-03-05T04:06:25.531328Z",
        "last_emitted_driver_frontend_event": "2025-03-05T03:49:52.647174Z",
        "last_emitted_driver_frontend_no_longer_detected_event": "2025-03-05T04:06:25.531328Z",
        "last_emitted_nfs_frontend_event": "",
        "last_emitted_nfs_frontend_no_longer_detected_event": ""
    },
    "hosts": {
        "active_count": 16,
        "backends": {
            "active": 12,
            "total": 13
        },
        "clients": {
            "active": 4,
            "total": 4
        },
        "computes": {
            "active": 6,
            "total": 6
        },
        "drives": {
            "active": 6,
            "total": 7
        },
        "total_count": 17
    },
    "hot_spare": 0,
    "init_stage": "INITIALIZED",
    "init_stage_changed_time": "2025-02-12T12:07:39.104761Z",
    "io_nodes": {
        "active": 24,
        "total": 26
    },
    "io_status": "STARTED",
    "io_status_changed_time": "2025-02-12T12:07:46.080318Z",
    "is_cluster": true,
    "last_init_failure": "",
    "last_init_failure_code": "",
    "last_init_failure_time": "",
    "licensing": {
        "io_start_eligibility": true,
        "mode": "Unlicensed",
        "usage": {
            "data_reduction": false,
            "drive_capacity_gb": 353348,
            "obs_capacity_gb": 0,
            "usable_capacity_gb": 176253
        }
    },
    "long_drive_grace_on_failure_secs": 120,
    "name": "weka-infra",
    "net": {
        "link_layer": "ETH"
    },
    "nodes": {
        "blacklisted": 0,
        "total": 47
    },
    "overlay": {
        "backend_nodes_safety_histogram": [
            0,
            0,
            34,
            1
        ],
        "branching_factor": 40,
        "client_nodes_at_risk": 0,
        "client_nodes_not_supported": 0,
        "client_nodes_safety_histogram": [
            0,
            0,
            0,
            8
        ],
        "clients_branching_factor": 200,
        "format": 3,
        "max_supported_client_nodes": 5200
    },
    "processes": {
        "blacklisted": 0,
        "total": 47
    },
    "rebuild": {
        "enoughActiveFDs": true,
        "isInited": true,
        "movingData": false,
        "movingQuorums": false,
        "numActiveFDs": 6,
        "numBucketsInReport": 156,
        "numBucketsTotal": 156,
        "numProtectionDisks": 2,
        "potentialResilience": 2,
        "progressPercent": 0,
        "protectionState": [
            {
                "MiB": 4952064,
                "numFailures": 0,
                "percent": 100
            },
            {
                "MiB": 0,
                "numFailures": 1,
                "percent": 0
            },
            {
                "MiB": 0,
                "numFailures": 2,
                "percent": 0
            }
        ],
        "requiredFDsForRebuild": 3,
        "scrubberBytesPerSec": 536870912,
        "stickyProtectionState": [
            {
                "MiB": 4952064,
                "numFailures": 0,
                "percent": 100
            },
            {
                "MiB": 0,
                "numFailures": 1,
                "percent": 0
            },
            {
                "MiB": 0,
                "numFailures": 2,
                "percent": 0
            }
        ],
        "stickyUnavailableMiB": 0,
        "stickyUnavailablePercent": 0,
        "stripeDisks": 5,
        "totalCopiesDoneMiB": 0,
        "totalCopiesMiB": 0,
        "unavailableMiB": 0,
        "unavailablePercent": 0
    },
    "release": "4.4.2.157-k8s.2",
    "release_hash": "885aee725dfc9dbf83ee2998e375396b27f74fdf",
    "response_from_host_id": 100,
    "scrubber_bytes_per_sec": 536870912,
    "servers": {
        "active": 10,
        "backends": {
            "active": 6,
            "degraded": 0,
            "total": 7
        },
        "clients": {
            "active": 4,
            "degraded": 0,
            "total": 4
        },
        "degraded": 0,
        "total": 11
    },
    "short_drive_grace_on_failure_secs": 10,
    "start_io_starting_drives_grace_secs": 30,
    "start_io_starting_io_nodes_grace_secs": 30,
    "status": "OK",
    "stripe_data_drives": 3,
    "stripe_protection_drives": 2,
    "time": {
        "allowed_clock_skew_secs": 60,
        "cluster_local_utc_offset": "+00:00",
        "cluster_local_utc_offset_seconds": 0,
        "cluster_time": "2025-04-09T11:54:25.9790869Z"
    },
    "upgrade_info": {
        "upgrade_paused": false,
        "upgrade_phase": "",
        "upgrade_type": "agent-driven"
    },
    "wide_range_nodes_allocation": true
}
```

### Field Descriptions

This JSON output provides comprehensive information about the cluster, including:

- Cluster status and health metrics
- Capacity information
- Node and drive status
- Network configuration
- Rebuild and protection state
- Activity metrics
- Licensing information
- Time synchronization details
- And more

Refer to this example when building automation tools that interface with Weka clusters or when troubleshooting cluster issues.