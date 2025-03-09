  S3 containers are also wekacontainers, and will have weka.io/mode=s3
    `weka s3 cluster status` shows how many containers in what status are registered on weka side as serving S3
    It might take up to 3 minutes for them to become operational
  Provides output as:
    ```aiignore
    ID  HOSTNAME  S3 STATUS  IP           PORT   VERSION    UPTIME    ACTIVE REQUESTS  LAST FAILURE
    16  H1-7-C    Online     10.200.5.67  37300  4.4.2.157  0:04:11h  0
    17  H1-7-A    Online     10.200.5.65  37300  4.4.2.157  0:04:10h  0
    18  H1-6-B    Online     10.200.5.62  37300  4.4.2.157  0:02:56h  0
    19  H1-5-D    Online     10.200.5.60  37300  4.4.2.157  0:03:04h  0
    20  H1-6-A    Online     10.200.5.61  37300  4.4.2.157  0:02:56h  0
    ```
    Online status is healthy, and means that S3 containers are operational
