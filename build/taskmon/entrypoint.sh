#!/bin/sh

set -e

env | grep -i TASKMON || true
exec /taskmon/taskmon start
