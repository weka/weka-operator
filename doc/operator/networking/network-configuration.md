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

### IPv4 / IPv6 Selection
The address family is inferred from the configuration; `spec.ipv6` is only a tiebreaker.

| Configuration | Address family used |
| --- | --- |
| `deviceSubnets` (or a selector `subnet`) is an IPv6 CIDR | IPv6 |
| `deviceSubnets` (or a selector `subnet`) is an IPv4 CIDR | IPv4, even if `spec.ipv6: true` |
| `ethDevice`/`ethDevices` whose interface holds only one family | that family |
| `ethDevice`/`ethDevices` whose interface is dual-stack | IPv4, or IPv6 when `spec.ipv6: true` |

A subnet always pins the family, so `spec.ipv6` is only needed for a dual-stack interface selected by
name. IPv6 addresses must be ULA or GUA — Weka does not use link-local addresses, so an interface that
holds only an `fe80::` address is treated as having no IPv6 address.

```yaml
spec:
  network:
    deviceSubnets:
      - 2001:db8::/64
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
   netmask	<integer>
   udpMode	<boolean>
```

## Best Practices
- For cloud environments, network configuration is typically optional
- For physical environments, always specify appropriate network configuration
- Choose the most appropriate network device selection method based on your infrastructure:
  - `ethDevice`: For single-device configurations
  - `ethDevices`: For multi-device configurations
  - `deviceSubnets`: For subnet-based selection