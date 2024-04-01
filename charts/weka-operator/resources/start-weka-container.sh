#!/bin/bash

set -o pipefail
set -e
# Starts weka container user mode components (frontend, compute, drive). Agent assumed to be running.

# Path to the directory housing the scripts

# Hacks around drivers compilation
WEKA_DRIVER_VERSION="1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86"
IGB_UIO_DRIVER_VERSION="weka1.0.2"
MPIN_USER_DRIVER_VERSION="1.0.1"
UIO_PCI_GENERIC_DRIVER_VERSION=5f49bb7dc1b5d192fb01b442b17ddc0451313ea2

OS=$(uname)

date_print() {

  if [ "$OS" = "Linux" ]; then
    date '+%Y-%m-%d %H:%M:%S'
  elif [ "$OS" = "Darwin" ]; then  # macOS
    date -u "+%Y-%m-%d %H:%M:%S"
  fi
}

ts() {
  while read LINE; do
    echo "$(date_print)$* $LINE"
  done
}

export GRAY="\033[1;30m"
export LIGHT_GRAY="\033[0;37m"
export CYAN="\033[0;36m"
export LIGHT_CYAN="\033[1;36m"
export PURPLE="\033[1;35m"
export YELLOW="\033[1;33m"
export LIGHT_RED="\033[1;31m"
export NO_COLOUR="\033[0m"

MYNAME=$(basename $0)

log_message() {
  # just add timestamp and some coloring
  local LEVEL COLOR
  [[ ${1} =~ TRACE|DEBUG|INFO|NOTICE|WARN|WARNING|ERROR|CRITICAL|FATAL ]] && LEVEL="${1}" && shift || LEVEL="INFO"

  case $LEVEL in
    DEBUG) COLOR="$LIGHT_GRAY" ;;
    INFO) COLOR="$CYAN" ;;
    NOTICE) COLOR="$PURPLE" ;;
    WARNING | WARN) COLOR="$YELLOW" ;;
    ERROR | CRITICAL) COLOR="$LIGHT_RED" ;;
  esac

  ts "$(echo -e "$COLOR") $(echo -e "${LEVEL}$NO_COLOUR") [$MYNAME:${BASH_LINENO[$((${#BASH_LINENO[@]} - 2))]}]"$'\t' <<<"$*" | tee -a $LOG_FILE
}


log_fatal() {
  log_message CRITICAL "$@"
  exit 1
}

log_pipe() {
  ts "$(echo -e "$LIGHT_CYAN") $(echo -e "STDOUT${NO_COLOUR}") [$MYNAME]"$'\t' | tee -a $LOG_FILE
}

log_pipe_err() {
  ts "$(echo -e "$LIGHT_RED") $(echo -e "STDERR${NO_COLOUR}") [$MYNAME]"$'\t' | tee -a $LOG_FILE
}

#exec 2> >(tee -a /tmp/start-stderr >&2)
#exec 1> >(tee -a /tmp/start-stdout)

wait_for_syslog() {
  while ! [ -f /var/run/syslog-ng.pid ]; do
    sleep 0.1
    echo "Waiting for syslog-ng to start"
  done
}

time wait_for_syslog 2> >(log_pipe_err >&2) | log_pipe

log_message INFO "Starting WEKA-CONTAINER"
# should be param from outside, not part of generic configmap

# Define used variables/defaults
MODE=${MODE}
PORT=${PORT}
MEMORY=${MEMORY}
CORE_IDS=${CORE_IDS}
CORES=${CORES}
NAME=${NAME}
NETWORK_DEVICE=${NETWORK_DEVICE}
DIST_SERVICE=${DIST_SERVICE:-"http://localhost:60002"}


# Print out parameters from environment variables.
log_message INFO "NAME=${NAME}"
log_message INFO "MODE=${MODE}"
log_message INFO "AGENT_PORT=${AGENT_PORT}"
log_message INFO "PORT=${PORT}"
log_message INFO "WEKA_PORT=${WEKA_PORT}"
log_message INFO "MEMORY=${MEMORY}"
log_message INFO "CORE_IDS=${CORE_IDS}"
log_message INFO "CORES=${CORES}"
log_message INFO "NETWORK_DEVICE=${NETWORK_DEVICE}"
log_message INFO "WEKA_CLI_DEBUG=${WEKA_CLI_DEBUG}"

EXITING=0
stop() {
  log_message WARNING "Received a stop signal. Exiting, Weka Container will be stopped by Agent"
  EXITING=1
  exit 127
}

trap stop SIGINT SIGTERM

wait_for_shutdown() {
  while [ $EXITING -eq 0 ]; do
    sleep 1 &
    wait
  done
}

# Logic start

copy_drivers(){
#  ALL_DRIVERS=`curl localhost:14000/dist/v1/release/$(weka version current).spec | jq -r '.containers.weka.images | keys | .[]'`
#  LOCAL_DRIVERS=`find /opt/weka/data -name "*.ko" | grep $(uname -r)`

#  WEKA_DRIVER=`echo -n $ALL_DRIVERS | xargs -n1 echo | grep weka-driver-` # broke
  # wekafs driver is the only that somehow keeps versioning betweeen data dir and spec.
#  WEKAFS_BUILD_DIR=`echo -n $WEKA_DRIVER | sed -e 's/weka-driver-//g' -e 's/.squashfs//g'` # 1.0.0-995f26b334137fd78d57c264d5b19852

  cp /opt/weka/data/weka_driver/${WEKA_DRIVER_VERSION}/`uname -r`/wekafsio.ko /opt/weka/dist/drivers/weka_driver-wekafsio-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
  cp /opt/weka/data/weka_driver/${WEKA_DRIVER_VERSION}/`uname -r`/wekafsgw.ko /opt/weka/dist/drivers/weka_driver-wekafsgw-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko

  cp /opt/weka/data/igb_uio/${IGB_UIO_DRIVER_VERSION}/`uname -r`/igb_uio.ko /opt/weka/dist/drivers/igb_uio-$IGB_UIO_DRIVER_VERSION-`uname -r`.`uname -m`.ko
  cp /opt/weka/data/mpin_user/${MPIN_USER_DRIVER_VERSION}/`uname -r`/mpin_user.ko /opt/weka/dist/drivers/mpin_user-$MPIN_USER_DRIVER_VERSION-`uname -r`.`uname -m`.ko
  cp /opt/weka/data/uio_generic/${UIO_PCI_GENERIC_DRIVER_VERSION}/`uname -r`/uio_pci_generic.ko /opt/weka/dist/drivers/uio_pci_generic-$UIO_PCI_GENERIC_DRIVER_VERSION-`uname -r`.`uname -m`.ko
}

load_drivers(){
  curl -fo /opt/weka/dist/drivers/weka_driver-wekafsgw-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko ${DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsgw-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko || return 1
  curl -fo /opt/weka/dist/drivers/weka_driver-wekafsio-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko ${DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsio-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko || return 1
  curl -fo /opt/weka/dist/drivers/igb_uio-$IGB_UIO_DRIVER_VERSION-`uname -r`.`uname -m`.ko ${DIST_SERVICE}/dist/v1/drivers/igb_uio-$IGB_UIO_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1
  curl -fo /opt/weka/dist/drivers/mpin_user-$MPIN_USER_DRIVER_VERSION-`uname -r`.`uname -m`.ko ${DIST_SERVICE}/dist/v1/drivers/mpin_user-$MPIN_USER_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1
  curl -fo /opt/weka/dist/drivers/uio_pci_generic-$UIO_PCI_GENERIC_DRIVER_VERSION-`uname -r`.`uname -m`.ko ${DIST_SERVICE}/dist/v1/drivers/uio_pci_generic-$UIO_PCI_GENERIC_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1

  lsmod | grep wekafsgw || insmod /opt/weka/dist/drivers/weka_driver-wekafsgw-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko || return 1
  lsmod | grep wekafsio || insmod /opt/weka/dist/drivers/weka_driver-wekafsio-${WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko || return 1
  lsmod | grep uio || modprobe uio
  lsmod | grep igb_uio || insmod /opt/weka/dist/drivers/igb_uio-$IGB_UIO_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1
  lsmod | grep mpin_user || insmod /opt/weka/dist/drivers/mpin_user-$MPIN_USER_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1
  lsmod | grep uio_pci_generic || insmod /opt/weka/dist/drivers/uio_pci_generic-$UIO_PCI_GENERIC_DRIVER_VERSION-`uname -r`.`uname -m`.ko || return 1
}


if [[ "$MODE" == "drivers-loader" ]]; then
  while ! load_drivers; do
    sleep 1
    echo "Retrying drivers load"
  done
  echo "Drivers loaded successfully"
  wait_for_shutdown
  exit 0
fi

wait_for_agent() {
  while ! [ -f /var/run/weka-agent.pid ]; do
    sleep 1
    echo "Waiting for weka-agent to start"
  done
}

# weka container start

while ! weka local ps; do
  log_message INFO "Waiting for agent to start"
  sleep 1
done


if [[ $(weka local ps | sed 1d | wc -l) != "0" ]]; then
  log_message INFO "Weka container already exists, doing nothing, as agent will start it"
  if [[ "$MODE" == "dist" ]]; then
    copy_drivers # this is getting out of hand, need completely separate scripts/set of functions with switch per case
  fi
  wait_for_shutdown
  exit 0
fi


time wait_for_agent 2> >(log_pipe_err >&2) | log_pipe
weka version set $(weka version | tee /dev/stderr) 2> >(log_pipe_err >&2) | log_pipe

if [[ "$MODE" == "dist" ]]; then
    weka version prepare `weka version current` || weka local setup container --name dist --net udp --base-port ${PORT}
    copy_drivers
    wait_for_shutdown
    exit 0
fi


if [[ -z "${CORE_IDS}" || "$CORE_IDS" == "auto" ]]; then
  if [[ "$MODE" != "dist" ]]; then
    log_fatal "CORE_IDS 'auto' is not supported yet. Please specify a comma-separated list of core ids to use."
  fi
fi
#
#CURRENT_CORE=""
#pop_core() {
#    if [[ "$CORE_IDS" != "auto" ]]; then
#      CURRENT_CORE=${CORE_IDS}
#      return
#    fi
#    local popped_element=${core_array[0]} # Get the first element
#    unset core_array[0] # Remove the first element from the array
#    core_array=("${core_array[@]}") # Re-index the array
#    CORE_IDS="${core_array[*]}" # Update CORE_IDS with remaining elements
#    CURRENT_CORE=$popped_element
#}
#
#if [[ "$CORE_IDS" == "auto" ]]; then
#    CORE_IDS=$(/opt/available_core_ids.sh)
#    IFS=' ' read -r -a core_array <<< "$CORE_IDS"
#fi


# Differentiate between different execution modes
CLI=weka
JOIN_IPS_BLOCK=""
case "$MODE" in
  "drive")
    MODE_SELECTOR="--only-drives-cores"
    ;;
  "compute")
    MODE_SELECTOR="--only-compute-cores"
    ;;
  "client")
    MODE_SELECTOR="--only-frontend-cores"
    JOIN_IPS_BLOCK="--join-ips ${JOIN_IPS}"
    ;;
  "dist")
    echo "dist should not enter standard flow and handled earlier"
    exit 1
    ;;
  "drivers-loader")
    echo "drivers-loader should not enter standard flow and handled earlier"
    exit 1
    ;;
  *)
    log_fatal "Invalid mode ($MODE) specified. Please use either 'drive', 'compute', 'client' only."
    ;;
esac

log_message INFO "Starting weka container with the following configuration:"
log_message INFO weka local setup container --name ${NAME} --no-start --net ${NETWORK_DEVICE} --cores ${CORES} ${MODE_SELECTOR} --base-port ${PORT} --memory ${MEMORY} --core-ids ${CORE_IDS} ${JOIN_IPS_BLOCK}
weka local setup container --name ${NAME} --no-start --net ${NETWORK_DEVICE} --cores ${CORES} ${MODE_SELECTOR} --base-port ${PORT} --memory ${MEMORY} --core-ids ${CORE_IDS} ${JOIN_IPS_BLOCK} 2> >(log_pipe >&2) | log_pipe
weka local resources --json | jq ".reserve_1g_hugepages=false" > /tmp/new_resources.json
weka local resources import --force /tmp/new_resources.json
weka local resources apply --force

if [[ $? -ne 0 ]]; then
  log_fatal "Failed to start weka container"
fi
log_message NOTICE "Successfully started weka container."

# Sleep forever
#bash -ce "tail -F /opt/weka/logs/${NAME}/weka/output.log | tee -a $LOG_FILE" &
wait_for_shutdown