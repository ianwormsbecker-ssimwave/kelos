#!/bin/sh
# glab wrapper for Kelos agent containers.
#
# Installed at /usr/local/bin/glab so it shadows the release binary at
# /usr/bin/glab. On each invocation, if KELOS_GITLAB_TOKEN_FILE is set
# and readable, the wrapper exports the file contents as GITLAB_TOKEN
# (the variable glab reads for authentication), then execs the real
# glab. This lets a workspace Secret rotation propagate to the
# long-running agent process without it picking up stale env vars.

set -u

if [ -n "${KELOS_GITLAB_TOKEN_FILE:-}" ] && [ -r "${KELOS_GITLAB_TOKEN_FILE}" ]; then
  GITLAB_TOKEN=$(cat "${KELOS_GITLAB_TOKEN_FILE}")
  export GITLAB_TOKEN
fi

exec /usr/bin/glab "$@"
