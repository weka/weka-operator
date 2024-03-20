#!/bin/bash


AGENT_PORT=${AGENT_PORT}

stop() {
  echo "Received stop signal"
  echo "Stopping Weka Agent"
  weka local stop
  kill -9 $WEKA_AGENT_PID
  echo "Weka Agent stopped"
  exit 127
}
trap stop SIGTERM SIGINT


echo "Starting Weka Agent"

echo "Disabling cgroups"
sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true

echo "Setting agent port to ${AGENT_PORT}"
sed -i "s/port=14100/port=${AGENT_PORT}/g" /etc/wekaio/service.conf || true

echo '{"agent": {"port": '${AGENT_PORT}'}}' > /etc/wekaio/service.json

/usr/bin/weka --agent --socket-name weka_agent_ud_socket_${AGENT_PORT} &
WEKA_AGENT_PID=$!
echo "Weka Agent started"


wait


