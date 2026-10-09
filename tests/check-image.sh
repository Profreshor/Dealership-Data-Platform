#!/bin/sh
set -eu
python3 "$(dirname "$0")/proving-ground/docker-host-test.py"
exec python3 "$(dirname "$0")/proving-ground/docker-host.py" "$@"
