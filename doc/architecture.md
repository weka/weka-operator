# Architecture

## Operator Daemonset

### Agent Container

### Driver Init-Container

## Driver Management

Drivers are expected to be archived on a backend node.
The driver init-container will download the driver from the backend node and install it on the host.
Since the init-container is part of the Daemonset, we can assume that the driver is installed on all nodes.

The init container will use the `BACKEND_PRIVATE_IP` to construct the download URL.
Drivers must be available on this machine at `/opt/weka/dist/drivers`.
Drivers are assumed to be `.tar.gz` files named `<driver name>-<weka version>-<kernel version>-<arch>.tar.gz`.
Note: since arch is typically also part of the kernel version, the file name will duplicate this information.
