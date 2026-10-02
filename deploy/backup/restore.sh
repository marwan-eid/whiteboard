#!/bin/sh
# Restores the whiteboard database from a backup made by backup.sh.
#   restore.sh /backups/whiteboard-<time>.dump
#   restore.sh https://...           (a URL that can read the dump)
# Stop the nodes first, so nothing writes during the restore:
#   docker compose -f compose.yaml -f deploy/compose.prod.yaml stop node-1 node-2
#   docker compose -f compose.yaml -f deploy/compose.prod.yaml run --rm --entrypoint sh backup /backup/restore.sh <dump>
#   docker compose -f compose.yaml -f deploy/compose.prod.yaml start node-1 node-2
set -eu
src=${1:?usage: restore.sh <dump file or URL>}
start=$(date +%s)
if [ "${src#http}" != "$src" ]; then
  command -v curl >/dev/null || apk add --no-cache curl >/dev/null
  curl --fail --silent --show-error -o /tmp/restore.dump "$src"
  src=/tmp/restore.dump
fi
# Replace everything the dump contains; objects it lacks are dropped first.
pg_restore --clean --if-exists --no-owner --single-transaction --dbname="$PGDATABASE" "$src"
echo "restore: done in $(( $(date +%s) - start )) s"
psql -At -c "SELECT 'restore: ' || count(*) || ' boards, ' || (SELECT count(*) FROM ops) || ' log rows, ' || (SELECT count(*) FROM snapshots) || ' snapshots' FROM boards"
