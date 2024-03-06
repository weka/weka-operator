#!/usr/bin/env bash

echo "Don't run this script"
exit 0 # Don't run this script

set -euo pipefail
IFS=$'\n\t'

# this is a quick hacky script to setup weka containers on a backend
# Copy it up to a server and run it there

# on mbp-be and mbp-be2
for i in {1..2}; do
  weka local setup container --name "c${i}" --cores 1 --core-ids "${i}" \
    --memory 20GiB --failure-domain "fd${i}" --base-port "2${i}000" --net mlnx0
done

# TODO: on mbp-be3

# Create cluster
# TODO: need 5th node
weka cluster create -P 21000 c{1..6} --host-ips 10.222.67.0:21000,10.222.67.0:22000,10.222.95.0:23000,10.222.95.0:24000,10.222.62.0:25000,10.222.62.0:26000

# Add Drives
export WEKA_PORT=21000
weka cluster drive add 0 /dev/nvme0n1
weka cluster drive add 1 /dev/nvme1n1
weka cluster drive add 2 /dev/nvme0n1
weka cluster drive add 3 /dev/nvme1n1
weka cluster drive add 4 /dev/nvme0n1
weka cluster drive add 5 /dev/nvme1n1

weka cluster start-io
