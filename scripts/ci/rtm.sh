#!/usr/bin/env bash
# rtm.sh — Gate: sincronia da RTM com o corpus (AOS-186).
set -uo pipefail
python3 "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/rtm-regenerate.py" --check
