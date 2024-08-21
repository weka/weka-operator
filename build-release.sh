#!/bin/zsh

VERSION=$(npx semantic-release --ci false --dry-run | grep "The next release version is" | awk '{print $NF}')
