#!/bin/sh

set -e

env | grep -i TASKMON
exec /taskmon/taskmon start