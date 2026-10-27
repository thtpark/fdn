#!/bin/sh
set -eu
exec "$(dirname "$0")/dev-agent.sh" agent-identity "$@"
