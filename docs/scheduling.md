# Sheduling

Package: `internal/app/manager/domain/scheduling`

## Status

- Status: `In Progress`

Simple scheduling is implemented but it is limited to one weka container per node.

TODO: 
- Assign containers to drives and cores
- Maintain sticky assignment of containers to drives
- Distribute workloads across nodes
- Ensure sufficient memory before assigning a node

## Description

The scheduling package is responsible for assigning weka-containers to kubernetes nodes.
Sheduling takes into account the following factors:
- The availability of drives, cores, and memory
- If the workload was previous assigned a drive and thus a node
- Distribution of workloads across nodes
