#!/bin/bash

set -o pipefail
# Starts weka client (user mode components)
STATUS_FILE=/opt/weka-status  # this file may be used to check the status of the weka container
WEKACMD=/usr/bin/weka

# Path to the directory housing the scripts
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)

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

  ts "$(echo -e "$COLOR") $(echo -e "${LEVEL}$NO_COLOUR")"$'\t' <<<"$*" | tee -a $LOG_FILE
}


log_fatal() {
  log_message CRITICAL "$@"
  exit 1
}

log_pipe() {
  ts "$(echo -e "$LIGHT_CYAN") $(echo -e "STDOUT${NO_COLOUR}")"$'\t' | tee -a $LOG_FILE
}

log_pipe_err() {
  ts "$(echo -e "$LIGHT_RED") $(echo -e "STDERR${NO_COLOUR}")"$'\t' | tee -a $LOG_FILE
}

exec 2> >(tee -a /tmp/start-stderr >&2)
exec 1> >(tee -a /tmp/start-stdout)

log_message INFO "Starting WEKA-CONTAINER"
# should be param from outside, not part of generic configmap

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

WEKA_VERSION=$(weka version)
log_message INFO "Setting weka version to ${WEKA_VERSION}"
weka version set ${WEKA_VERSION} 2> >(log_pipe_err >&2) | log_pipe

if [[ -z "${CORE_IDS}" || "$CORE_IDS" == "auto" ]]; then
  log_fatal "CORE_IDS 'auto' is not supported yet. Please specify a comma-separated list of core ids to use."
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
case "$MODE" in
  "drive")
    MODE_SELECTOR="--only-drives-cores"
    ;;
  "compute")
    MODE_SELECTOR="--only-compute-cores"
    ;;
  "client")
    MODE_SELECTOR="--only-frontend-cores"
    ;;
  *)
    log_fatal "Invalid mode ($MODE) specified. Please use either 'drive', 'compute', 'client' only."
    ;;
esac

log_message INFO "Starting weka container with the following configuration:"
weka local setup container --name ${NAME} --net ${NETWORK_DEVICE} --cores ${CORES} ${MODE_SELECTOR} --base-port ${PORT} --memory ${MEMORY} --core-ids ${CORE_IDS} 2> >(log_pipe >&2) | log_pipe

if [[ $? -ne 0 ]]; then
  log_fatal "Failed to start weka container"
fi
log_message NOTICE "Successfully started weka container."

# Sleep forever
exec sleep infinity
