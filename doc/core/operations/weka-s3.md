# Weka S3 Guide

## Overview
This document provides information about S3 functionality within Weka clusters in Kubernetes. It covers S3 container status monitoring and operational details for managing S3 capabilities.

## S3 Container Types
S3 containers are specialized WekaContainers with mode `weka.io/mode=s3`. They provide S3-compatible object storage access to the Weka filesystem.

## Monitoring S3 Status
The `weka s3 cluster status` command shows how many containers are registered on the Weka side as serving S3. It provides visibility into the operational status of S3 services.

### Status Command Output
```
ID  HOSTNAME  S3 STATUS  IP           PORT   VERSION    UPTIME    ACTIVE REQUESTS  LAST FAILURE
16  H1-7-C    Online     10.200.5.67  37300  4.4.2.157  0:04:11h  0
17  H1-7-A    Online     10.200.5.65  37300  4.4.2.157  0:04:10h  0
18  H1-6-B    Online     10.200.5.62  37300  4.4.2.157  0:02:56h  0
19  H1-5-D    Online     10.200.5.60  37300  4.4.2.157  0:03:04h  0
20  H1-6-A    Online     10.200.5.61  37300  4.4.2.157  0:02:56h  0
```

### S3 Container Health
- "Online" status indicates that S3 containers are operational and healthy
- It may take up to 3 minutes for S3 containers to become operational after deployment

## Provisioning S3 Capabilities
To provision a Weka cluster with S3 support, add the following to your WekaCluster spec:

```yaml
spec:
  dynamicTemplate:
    s3Containers: 3  # Number of S3 containers to provision
```

## Accessing S3 Storage
S3 containers expose an S3-compatible endpoint that can be used with standard S3 clients. The endpoint is typically available at:

```
http://<s3-service-ip>:<port>
```

The port is typically 37300 by default, but may be configured differently depending on your deployment.