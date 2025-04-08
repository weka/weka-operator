# Networking Configuration

## Overview
This document outlines networking configuration options for Weka clusters in Kubernetes environments. Proper network configuration is essential for optimal performance and functionality, especially in physical environments.

## Physical Environment Networking
When deploying a cluster on a physical environment, the network section must be specified. Physical environments require explicit network device selection through one of these methods:
- `ethDevice` - Single network device
- `ethDevices` - Multiple network devices
- `deviceSubnets` - Device subnet specification

## Network Configuration Options

### Basic Network Configuration
```yaml
spec:
  network:
    deviceSubnets:
      - 10.200.0.0/16
```

### UDP Mode Configuration
In rare cases when a specific network device should be used in UDP mode:
```yaml
spec:
  network:
    ethDevice: "eth0"
    udpMode: true
```

## Available Network Configuration Fields
```yaml
KIND:     WekaCluster
VERSION:  weka.weka.io/v1alpha1

RESOURCE: network <Object>

DESCRIPTION:
     weka cluster network configuration

FIELDS:
   aws	<Object>
      deviceSlots	<[]integer>
   deviceSubnets	<[]string>
   ethDevice	<string>
   ethDevices	<[]string>
   ethSlots	<[]string>
   gateway	<string>
   udpMode	<boolean>
```

## Best Practices
- For cloud environments, network configuration is typically optional
- For physical environments, always specify appropriate network configuration
- Choose the most appropriate network device selection method based on your infrastructure:
  - `ethDevice`: For single-device configurations
  - `ethDevices`: For multi-device configurations
  - `deviceSubnets`: For subnet-based selection