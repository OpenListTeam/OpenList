#!/usr/bin/env sh
set -eu

: "${MASTER_REMOTE:=master}"
: "${MASTER_ROOT:=open-box}"
: "${RCLONE_CONFIG:=/config/rclone/rclone.conf}"

# IMPORTANT: rclone copy is used, never sync.
# Source deletions therefore do not delete objects already collected in master.
SOURCES="
google-personal-01
google-personal-02
google-workspace-01
google-shared-drive-01
dropbox-account-01
dropbox-account-02
onedrive-personal-01
onedrive-business-01
onedrive-business-02
"

mkdir -p /logs

for src in $SOURCES; do
  echo "[$(date -Iseconds)] collecting $src"
  rclone copy "${src}:" "${MASTER_REMOTE}:${MASTER_ROOT}/sources/${src}" \
    --config "$RCLONE_CONFIG" \
    --checksum \
    --metadata \
    --create-empty-src-dirs \
    --retries 5 \
    --low-level-retries 10 \
    --log-file "/logs/${src}.log" \
    --log-level INFO

done

echo "[$(date -Iseconds)] collection complete"
