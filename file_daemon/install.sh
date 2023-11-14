#!/bin/bash

# Strict mode
set -euo pipefail
IFS=$'\n\t'

WEKA_VERSION="4.2.5"
# TOKEN should be provided by the enrionment
TOKEN="${TOKEN:-}"
DOWNLOAD_URL="https://${TOKEN}@get.weka.io/dist/v1/install/${WEKA_VERSION}/${WEKA_VERSION}"

# Run the remote installer
# Output and exit code go to /jailbreak/output
trap 'echo $? > /jailbreak/output/exit_code' EXIT
curl -sSL "${DOWNLOAD_URL}" | sh > /jailbreak/output/stdout 2> /jailbreak/output/stderr

# Remove the current script from the crontab
rm /etc/cron.d/install_weka.cron