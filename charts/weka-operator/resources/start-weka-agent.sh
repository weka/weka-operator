#!/bin/bash


AGENT_PORT=${AGENT_PORT}

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

  ts "$(echo -e "$COLOR") $(echo -e "${LEVEL}$NO_COLOUR") [$MYNAME]"$'\t' <<<"$*" | tee -a $LOG_FILE
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


stop() {
  log_message WARNING "Received a stop signal. Stopping Weka Container"
  weka local stop 2> >(log_pipe >&2) | log_pipe

  log_message WARNING "Stopping Weka Agent"
  kill -SIGINT $WEKA_AGENT_PID
  wait $WEKA_AGENT_PID
  log_message NOTICE "Weka Agent stopped"
  exit 127
}

trap stop SIGTERM SIGINT


log_message INFO "Weka software was preinstalled in $WEKA_FACTORY_DIR"

ln -sf $WEKA_FACTORY_DIR/dist /opt/weka/dist

log_message INFO "Starting Weka Agent"

log_message DEBUG "Disabling cgroups"
sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true

log_message DEBUG "Setting agent port to ${AGENT_PORT}"
sed -i "s/port=14100/port=${AGENT_PORT}/g" /etc/wekaio/service.conf || true

log_message DEBUG "Setting wekanode to communicate with agent on port ${AGENT_PORT}"
echo '{"agent": {"port": '${AGENT_PORT}'}}' > /etc/wekaio/service.json

/usr/bin/weka --agent --socket-name weka_agent_ud_socket_${AGENT_PORT} &
WEKA_AGENT_PID=$!
log_message NOTICE "Weka Agent started with PID $WEKA_AGENT_PID"


wait $WEKA_AGENT_PID
log_message NOTICE "Weka Agent exited with code $?"

