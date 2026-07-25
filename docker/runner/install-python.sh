#!/bin/bash
set -e

TOOLCACHE="${RUNNER_TOOL_CACHE:-/opt/hostedtoolcache}"
MANIFEST_URL="https://raw.githubusercontent.com/actions/python-versions/main/versions-manifest.json"
MANIFEST=$(curl -sL "$MANIFEST_URL")

for MINOR in "$@"; do
  FULL=$(echo "$MANIFEST" | jq -r \
    "[.[] | select(.version | startswith(\"${MINOR}.\")) | select(.stable)] | sort_by(.version | split(\".\") | map(tonumber)) | last | .version")

  if [ -z "$FULL" ] || [ "$FULL" = "null" ]; then
    echo "WARN: no stable version found for Python ${MINOR}, skipping"
    continue
  fi

  # Find download URL for linux-24.04 x64 (matches actions-runner base image)
  DL_URL=$(echo "$MANIFEST" | jq -r \
    "[.[] | select(.version == \"${FULL}\")] | .[0].files[] | select(.platform == \"linux\" and .arch == \"x64\" and (.filename | contains(\"24.04\"))) | .download_url")

  # Fallback to 22.04 if 24.04 not available
  if [ -z "$DL_URL" ] || [ "$DL_URL" = "null" ]; then
    DL_URL=$(echo "$MANIFEST" | jq -r \
      "[.[] | select(.version == \"${FULL}\")] | .[0].files[] | select(.platform == \"linux\" and .arch == \"x64\") | .download_url" | head -1)
  fi

  if [ -z "$DL_URL" ] || [ "$DL_URL" = "null" ]; then
    echo "WARN: no linux-x64 download for Python ${FULL}, skipping"
    continue
  fi

  DEST="${TOOLCACHE}/Python/${FULL}/x64"
  echo "Installing Python ${FULL} from ${DL_URL} -> ${DEST}"
  sudo mkdir -p "${DEST}"
  curl -sL "${DL_URL}" | sudo tar xz -C "${DEST}"
  sudo touch "${DEST}.complete"
  echo "  done"
done
