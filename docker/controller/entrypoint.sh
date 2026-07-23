#!/bin/bash
set -e

if [ -z "$APP_PRIVATE_KEY_FILE" ]; then
  echo "FATAL: APP_PRIVATE_KEY_FILE not set"
  exit 1
fi

exec dockerscaleset \
  --url "${GHA_URL:-https://github.com/CheehooLabs}" \
  --name "${SCALE_SET_NAME:-cheehoo-runner}" \
  --app-client-id "${APP_CLIENT_ID}" \
  --app-installation-id "${APP_INSTALLATION_ID}" \
  --app-private-key "$(cat "$APP_PRIVATE_KEY_FILE")" \
  --max-runners "${MAX_RUNNERS:-8}" \
  --min-runners "${MIN_RUNNERS:-0}" \
  --log-level "${LOG_LEVEL:-info}"
