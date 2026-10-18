#!/usr/bin/env bash
# Fail when a CI step left tracked changes or files missing from .gitignore.
set -euo pipefail

status=$(git status -s)
if [ -n "$status" ]; then
    echo "There are uncommitted changes"
    echo "$status"
    exit 1
fi
