#!/usr/bin/env bash
# shellcheck shell=bash

# Strict mode
set -euo pipefail
IFS=$'\n\t'

# This script will simply copy the systemd service file to the correct location

# Arguments
# --mount-root: The root directory of the mount
# --target: Where to copy the file
while [[ $# -gt 0 ]]; do
    case "$1" in
        --target)
            TARGET="$2"
            shift 2
            ;;
        --app-dir)
            APP_DIR="$2"
            shift 2
            ;;
        *)
            echo "ERROR: Unknown argument $1"
            exit 1
            ;;
    esac
done

# Validate the arguments
if [ -z "${TARGET}" ]; then
    echo "ERROR: --target argument is required"
    exit 1
fi

if [ -z "${APP_DIR}" ]; then
    echo "ERROR: --app-dir argument is required"
    exit 1
fi

JAILBREAK_DIR="/jailbreak"

# install.sh gets copied to /tmp because we only use it once
cp "${APP_DIR}/install.sh" "${JAILBREAK_DIR}/install.sh"

# The crontab goes in /etc/cron.d
cp "${APP_DIR}/install_weka.cron" "/etc/cron.d/file_daemon"

# STDERR, STDOUT, and the return code all go in the output folder
mkdir -p "${JAILBREAK_DIR}/output"
