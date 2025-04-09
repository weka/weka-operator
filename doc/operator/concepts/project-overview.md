# Project Overview

## Overview
This document provides a comprehensive overview of the Weka Operator project for Kubernetes. It explains the core components, architecture, and the relationship between different resources in the Weka Kubernetes ecosystem.

## Core Components

The Weka Operator manages the following key resources:

### WekaCluster
- A Kubernetes custom resource representing a Weka storage cluster
- Consists of a set of WekaContainers that form the storage backend

### WekaContainer
- Kubernetes custom resource representing individual Weka containers
- Each WekaContainer is represented by a Pod in Kubernetes
- Each Pod contains a Weka agent that runs Weka Linux containers within the Pod

## Container Types
The operator supports multiple WekaContainer modes, each providing specific functionality:

```
WekaContainerModeDist           = "dist"           # Distribution mode
WekaContainerModeDriversDist    = "drivers-dist"   # Drivers distribution mode
WekaContainerModeDriversLoader  = "drivers-loader" # Drivers loader mode
WekaContainerModeDriversBuilder = "drivers-builder" # Drivers builder mode
WekaContainerModeCompute        = "compute"        # Compute node mode
WekaContainerModeDrive          = "drive"          # Drive node mode
WekaContainerModeClient         = "client"         # Client mode
WekaContainerModeDiscovery      = "discovery"      # Discovery mode
WekaContainerModeS3             = "s3"             # S3 gateway mode
WekaContainerModeNfs            = "nfs"            # NFS gateway mode
WekaContainerModeEnvoy          = "envoy"          # Envoy proxy mode
WekaContainerModeAdhocOpWC      = "adhoc-op-with-container" # Ad-hoc operation with container
WekaContainerModeAdhocOp        = "adhoc-op"       # Ad-hoc operation
```

## WekaContainer Lifecycle

- WekaContainer creation:
  - Created by WekaCluster controller, WekaClient controller, or manually in specific cases
  - Deployed in the same namespace as the WekaCluster
- Termination behavior:
  - On pod deletion, weka_runtime will block and wait for termination signal
  - Termination signals: `/tmp/.allow-stop` or `/tmp/.allow-force-stop`
  - Pods are recreated by the controller with updated configuration
- Critical operations:
  - Never force delete pods (`--force`, `--grace-period=X`, or `--timeout` flags should never be used)
  - Pod names match WekaContainer names and are recreated with the same name

## Resource Overrides
- WekaContainer, WekaCluster, and WekaClient CRs have an `overrides` field
- Overrides should only be updated by patching with merge strategy:
  - `kubectl patch wekacontainer container-name -n default --type='merge' -p='{"status":{"debugSleepOnTerminate": 300}}'`