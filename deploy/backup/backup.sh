#!/bin/sh
# Nightly backup of the whiteboard database (deploy/compose.prod.yaml).
#   backup.sh        run forever: one dump a day at BACKUP_AT (UTC)
#   backup.sh now    one dump, then exit
# Dumps (pg_dump custom format, compressed) go to /backups, which keeps the
# last 7, and to BACKUP_UPLOAD_URL if set: an OCI Object Storage
# pre-authenticated request URL ending in /o/, which can write but not read.
set -eu

command -v curl >/dev/null || apk add --no-cache curl >/dev/null

dump() {
  name="whiteboard-$(date -u +%Y%m%dT%H%M%SZ).dump"
  start=$(date +%s)
  pg_dump --format=custom --file="/backups/$name.part"
  mv "/backups/$name.part" "/backups/$name"
  size=$(wc -c < "/backups/$name")
  echo "backup: wrote $name ($size bytes) in $(( $(date +%s) - start )) s"
  if [ -n "${BACKUP_UPLOAD_URL:-}" ]; then
    curl --fail --silent --show-error --retry 3 -T "/backups/$name" "${BACKUP_UPLOAD_URL}$name"
    echo "backup: uploaded $name"
  fi
  # Keep the newest 7 locally; Object Storage keeps its own (lifecycle rule).
  ls -1t /backups/whiteboard-*.dump | tail -n +8 | xargs -r rm -f
}

if [ "${1:-}" = now ]; then
  dump
  exit 0
fi

at=${BACKUP_AT:-03:00}
while true; do
  now=$(date -u +%s)
  next=$(date -u -d "$(date -u +%Y-%m-%d) $at" +%s 2>/dev/null || date -u -D "%Y-%m-%d %H:%M" -d "$(date -u +%Y-%m-%d) $at" +%s)
  [ "$next" -gt "$now" ] || next=$((next + 86400))
  echo "backup: next at $at UTC, in $(( next - now )) s"
  sleep $((next - now))
  dump || echo "backup: FAILED"
done
