# IPv6

## Overview
Two address families are chosen independently:

- **The Kubernetes family**: the addresses Kubernetes gives nodes, pods and Services. On a dual-stack
  cluster the first family listed (`--node-ip`, pod and service CIDRs) is the primary one, and it is
  the one in `status.podIP`, node `InternalIP` and a Service's `clusterIP`.
- **The WEKA data-path family**: the addresses WEKA containers use for their own traffic, picked from
  the data NICs by `network` settings. See [IPv4 / IPv6 Selection](network-configuration.md#ipv4--ipv6-selection).

| Kubernetes | WEKA data path IPv4 | WEKA data path IPv6 |
| --- | --- | --- |
| IPv4-only or IPv4-primary | supported | supported |
| IPv6-primary (dual-stack) | supported | supported |
| IPv6-only | — (no IPv4 on the nodes) | supported |

## Requirements
- The data NICs carry a ULA or GUA address of the chosen WEKA family. Link-local (`fe80::`) addresses
  are ignored.
- Select the family with an IPv6 `deviceSubnets` entry (or selector `subnet`). `spec.ipv6` only breaks
  the tie on a dual-stack NIC selected by name.
- On an IPv6-only cluster, pods reach external endpoints (image registries aside, which the node
  pulls from) only over IPv6. Point `driversDistService` at the in-cluster drivers distribution service
  if `drivers.weka.io` is not reachable over IPv6; see [driver distribution](../deployment/driver-distribution.md).

## What changes on an IPv6 Kubernetes cluster
The operator reaches its components on their pod IPs: node agents, drivers builders and trace servers.
Those IPs are IPv6 here, and the operator brackets them in every URL and `host:port` it builds.

- **Drivers builder**: serves built drivers dual-stack whenever the node supports IPv6, so the
  operator can download from the builder's IPv6 pod IP. No configuration is involved; a builder
  created by a `WekaPolicy` never sets `spec.ipv6`.
- **Management proxy**: binds `::` when its Service gets an IPv6 primary family, `0.0.0.0` otherwise.
  List the same family first in the service and pod CIDRs of a dual-stack cluster, or the proxy's
  probes fail. See [address family](management-proxy.md#address-family).

## Checking the families
```bash
kubectl get nodes -o wide                                      # INTERNAL-IP: primary family
kubectl get svc kubernetes -o jsonpath='{.spec.ipFamilies}'    # ["IPv6"] or ["IPv6","IPv4"] on IPv6-primary
kubectl get wekacontainer -A -o wide                           # MANAGEMENT IPS: WEKA data-path family
```

## Related
- [Network configuration](network-configuration.md)
- [Management proxy](management-proxy.md)
