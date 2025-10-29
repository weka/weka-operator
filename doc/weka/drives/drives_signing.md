# WEKA Drive Signing and Investigation Guide

## Overview

WEKA uses a drive signing mechanism to claim ownership of storage devices and prevent accidental data loss or cross-cluster contamination. This guide explains how drive signing works and how to investigate drives on a node.

## Table of Contents

1. [How WEKA Drive Signing Works](#how-weka-drive-signing-works)
2. [Drive States and Visibility](#drive-states-and-visibility)
3. [Drive Signatures Explained](#drive-signatures-explained)
4. [Investigating Drives on a Node](#investigating-drives-on-a-node)
5. [Common Scenarios](#common-scenarios)
6. [Troubleshooting](#troubleshooting)

## How WEKA Drive Signing Works

### What is Drive Signing?

Drive signing is WEKA's method of "claiming" storage devices for exclusive use by a specific WEKA cluster. When a drive is signed:

1. **A WEKA partition is created** with a special partition type GUID
2. **The cluster ID is written** as a signature into the drive
3. **WEKA takes exclusive control** of the device at the kernel level

### The Signing Process

```
Raw Drive → Sign Drive → WEKA Partition Created → Drive Ready for WEKA
    ↓              ↓              ↓                    ↓
Visible to     Python signs    Partition type:      Drive can be
Linux tools    the drive       993ec906-...         used by WEKA
```

### Key Components

- **Partition Type GUID**: `993ec906-b4e2-11e7-a205-a0a8cd3ea1de` (identifies WEKA partitions)
- **Drive Signature**: The cluster ID (without dashes) written to the drive
- **Python Runtime**: Handles the actual signing process (`weka_runtime.py`)
- **Go Controller**: Manages the signing workflow and validation

## Drive States and Visibility

### Drive Visibility Matrix

| Drive State | Visible to Linux | Visible to WEKA | Can be Investigated |
|-------------|------------------|-----------------|-------------------|
| **Unsigned** | ✅ Yes | ❌ No | ✅ Yes |
| **Signed but Inactive** | ✅ Yes | ✅ Yes | ✅ Yes |
| **Signed and Active** | ❌ No | ✅ Yes | ❌ No* |

*Active drives are invisible to standard Linux tools because WEKA has exclusive kernel-level control.

### State Transitions

```
Unsigned Drive
    ↓ (sign-drives operation)
Signed Drive (Inactive)
    ↓ (weka cluster drive activate)
Active Drive (Invisible to Linux)
    ↓ (weka cluster drive deactivate)
Inactive Drive (Visible again)
    ↓ (force-resign-drives operation)
Unsigned Drive (Signature erased)
```

## Drive Signatures Explained

### What is a Drive Signature?

A drive signature is the **cluster ID written to the drive without dashes**.

**Example:**
- Cluster ID: `83fc8ec1-b7e7-4288-bc6f-1b9df281ebc1`
- Drive Signature: `83fc8ec1b7e74288bc6f1b9df281ebc1`

### How Signatures are Used

1. **Ownership Identification**: Each drive knows which cluster it belongs to
2. **Conflict Prevention**: Prevents drives from being accidentally used by wrong clusters
3. **Recovery**: Helps identify orphaned drives during cluster recovery

### Code Implementation

WEKA compares drive signatures by stripping dashes from cluster IDs to match the drive signature format. This allows the system to identify which drives belong to which cluster.

## Investigating Drives on a Node

### Prerequisites

- Root access to the node
- Basic understanding of Linux block devices
- WEKA CLI access (optional)

### Investigation Script

Save this script as `weka_investigate_drives.sh`:

```bash
#!/bin/bash

echo "=== WEKA Drive Investigation ==="
echo

# List devices from /dev/disk/by-id/ and /dev/disk/by-path/
echo "Devices by ID (first 10):"
ls /dev/disk/by-id/ 2>/dev/null | head -10

echo
echo "Devices by path (first 10):"
ls /dev/disk/by-path/ 2>/dev/null | head -10

echo
echo "=== Checking for WEKA partitions ==="

# Get all partition names (similar to the Python code)
part_names=()

# From by-path
if [ -d "/dev/disk/by-path" ]; then
  for device in /dev/disk/by-path/*; do
    if [ -L "$device" ]; then
      part_name=$(basename $(readlink -f "$device") 2>/dev/null)
      if [ -n "$part_name" ]; then
        part_names+=("$part_name")
      fi
    fi
  done
fi

# From by-id (avoiding duplicates)
for device in /dev/disk/by-id/*; do
  if [ -L "$device" ]; then
    part_name=$(basename $(readlink -f "$device") 2>/dev/null)
    if [ -n "$part_name" ] && [[ ! " ${part_names[@]} " =~ " ${part_name} " ]]; then
      part_names+=("$part_name")
    fi
  fi
done

echo "Found partitions: ${part_names[@]}"
echo

# Check each partition for WEKA signature
for part_name in "${part_names[@]}"; do
  echo "=== Checking /dev/$part_name ==="
  
  # Get partition type
  type_id=$(blkid -s PART_ENTRY_TYPE -o value -p "/dev/$part_name" 2>/dev/null)
  echo "  Partition type: $type_id"
  
  # Check if it's a WEKA partition
  if [ "$type_id" = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de" ]; then
    echo "  *** WEKA PARTITION FOUND ***"
    
    # Check the signature using hexdump
    echo "  Drive signature (offset 8, 16 bytes):"
    signature=$(hexdump -v -e '1/1 "%.2x"' -s 8 -n 16 "/dev/$part_name" 2>/dev/null)
    echo "    $signature"
    
    # Format as cluster ID (add dashes)
    if [ ${#signature} -eq 32 ]; then
      formatted_id="${signature:0:8}-${signature:8:4}-${signature:12:4}-${signature:16:4}-${signature:20:12}"
      echo "    Formatted cluster ID: $formatted_id"
    fi
    
    # Get device information
    echo "  Block device info:"
    pci_device_path=$(readlink -f "/sys/class/block/$part_name" 2>/dev/null)
    echo "    PCI path: $pci_device_path"
    
    if [[ "$part_name" == *"nvme"* ]]; then
      # NVMe device
      serial_id_path=$(echo "$pci_device_path" | sed 's|/[^/]*$||' | sed 's|/[^/]*$||')/serial
      if [ -f "$serial_id_path" ]; then
        serial_id=$(cat "$serial_id_path" 2>/dev/null)
        echo "    Serial ID: $serial_id"
      fi
      device_path="/dev/$(echo "$pci_device_path" | sed 's|.*/||' | sed 's|p[0-9]*$||')"
      echo "    Device path: $device_path"
    else
      # Regular SCSI device
      device_name=$(echo "$pci_device_path" | sed 's|.*/||' | sed 's|[0-9]*$||')
      device_path="/dev/$device_name"
      echo "    Device path: $device_path"
    fi
    
    echo
  fi
done

echo "=== Summary ==="
echo "WEKA partition type: 993ec906-b4e2-11e7-a205-a0a8cd3ea1de"
echo "Drive signatures are cluster IDs without dashes"
echo "Only inactive/deactivated WEKA drives are visible to this investigation"
```

### Running the Investigation

```bash
# Make the script executable
chmod +x weka_investigate_drives.sh

# Run the investigation
./weka_investigate_drives.sh
```

### Manual Commands

#### Find WEKA Partitions
```bash
# Find all WEKA partitions
for device in $(lsblk -dpno NAME); do 
  type_id=$(blkid -s PART_ENTRY_TYPE -o value -p $device 2>/dev/null)
  if [ "$type_id" = "993ec906-b4e2-11e7-a205-a0a8cd3ea1de" ]; then
    echo "WEKA partition: $device"
  fi
done
```

#### Check Drive Signature
```bash
# Check signature of a specific partition
hexdump -v -e '1/1 "%.2x"' -s 8 -n 16 /dev/your_partition_here

# More readable hex dump
hexdump -C -s 8 -n 16 /dev/your_partition_here
```

#### Compare with WEKA Cluster
```bash
# Get active drives from WEKA
weka cluster drive -o serial,hostname,status

# Get cluster ID
weka status | grep "Cluster ID"
```

## Common Scenarios

### Scenario 1: Fresh Node with Unsigned Drives
**What you'll see:**
- Drives visible in Linux (`lsblk`, `fdisk -l`)
- No WEKA partition type found
- No drive signatures

**What this means:**
- Drives are available for WEKA signing
- No previous WEKA installation

### Scenario 2: Node with Signed but Inactive Drives
**What you'll see:**
- WEKA partitions with type `993ec906-b4e2-11e7-a205-a0a8cd3ea1de`
- Drive signatures matching cluster IDs
- Drives visible in Linux

**What this means:**
- Drives were previously signed by WEKA
- Drives are currently not active in any cluster
- Drives can be reactivated or resigned

### Scenario 3: Node with Active WEKA Drives
**What you'll see:**
- Some drives missing from Linux block device listing
- `weka cluster drive` shows ACTIVE drives
- Investigation script doesn't find the active drives

**What this means:**
- WEKA has claimed these drives exclusively
- Normal Linux tools can't see them
- This is expected and correct behavior

### Scenario 4: Mixed Environment
**What you'll see:**
- Some drives visible (inactive/historical)
- Some drives invisible (currently active)
- Multiple different signatures

**What this means:**
- Node has history of multiple WEKA installations
- Some drives belong to current cluster, others are orphaned
- May need cleanup with `force-resign-drives`

## Troubleshooting

### Problem: Expected Drive Not Found

**Symptoms:**
- WEKA shows drive as active
- Investigation script doesn't find it

**Solution:**
- This is normal! Active drives are invisible to Linux
- Use `weka cluster drive` to see active drives
- Deactivate the drive temporarily to make it visible for investigation

### Problem: Multiple Signatures Found

**Symptoms:**
- Different signatures on different drives
- Some signatures don't match current cluster ID

**Solution:**
- These are likely orphaned drives from previous installations
- Use `force-resign-drives` operation to clean them up
- Verify which drives belong to current cluster

### Problem: Cannot Read Drive Signature

**Symptoms:**
- Permission denied errors
- hexdump fails

**Solution:**
- Ensure you have root access
- Check if drive is actually a WEKA partition
- Verify device path is correct

### Problem: Drive Shows Wrong Cluster ID

**Symptoms:**
- Drive signature doesn't match current cluster
- WEKA operator rejects the drive

**Solution:**
- Drive belongs to different cluster
- Use `force-resign-drives` to claim it for current cluster
- Verify this is intended (data will be lost)

## Understanding the Code Flow

### Drive Discovery Process
1. **Python Runtime** (`weka_runtime.py`) scans `/dev/disk/by-*` 
2. **Resolves device paths** to actual partition names
3. **Checks partition types** using `blkid`
4. **Reads signatures** from WEKA partitions
5. **Returns list** of discovered WEKA drives

### Drive Signing Process
1. **Go Controller** initiates signing operation
2. **Python Runtime** executes `sign_device_path()`
3. **Creates WEKA partition** with special GUID
4. **Writes cluster ID** as signature
5. **Drive becomes available** for WEKA use

### Drive Cleanup Process
1. **Container deletion** triggers `ResignDrives()`
2. **Drives are deactivated** first
3. **Force resign operation** erases signatures
4. **Drives become available** for reuse

This guide should help you understand and investigate WEKA drive signing behavior effectively!
