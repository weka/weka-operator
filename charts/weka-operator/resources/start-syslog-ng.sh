#!/bin/bash
OS=$(uname)

exec > /dev/stdout
exec 2>&1

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


stop() {
  log_message WARNING "Received a stop signal. Waiting for all processes to go down"
  while [ -f /var/run/weka-agent.pid ]; do
    sleep 1
  done

  log_message WARNING "Weka agent appears to be shut down. Resuming with own shutdown"

  log_message WARNING "Stopping Syslog-ng"
  kill -SIGINT $SYSLOG_PID
  log_message NOTICE "Syslog-ng stopped"
  exit 127
}

/usr/sbin/syslog-ng -F -f /etc/syslog-ng/syslog-ng.conf &
SYSLOG_PID=$!
echo $SYSLOG_PID > /var/run/syslog-ng.pid

trap stop SIGINT SIGTERM
wait $SYSLOG_PID

rm /var/run/syslog-ng.pid
