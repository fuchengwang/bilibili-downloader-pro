#!/usr/bin/env bash
# Formal builds run in GitHub Actions; distribution continues on this computer.
set -euo pipefail
cd "$(dirname "$0")/.."
exec uv run scripts/publish_release.py "$@"
