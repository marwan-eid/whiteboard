#!/bin/sh
# Updates the deployed stack to the latest images and configuration.
# Run on the server, in /opt/whiteboard:   sh deploy/update.sh [image tag]
# The tag defaults to IMAGE_TAG in .env (normally "latest"). Each release
# workflow run also publishes the commit SHA as a tag, for pinning or rollback.
set -eu
cd "$(dirname "$0")/.."
git pull --ff-only
if [ -n "${1:-}" ]; then
  sed -i "s/^IMAGE_TAG=.*/IMAGE_TAG=$1/" .env
fi
compose="docker compose -f compose.yaml -f deploy/compose.prod.yaml"
$compose pull
# Nodes restart one at a time: clients of the restarting node move to the
# other one (W8), so the board stays available.
$compose up -d --no-deps postgres prometheus grafana backup web
for node in node-1 node-2; do
  $compose up -d --no-deps --wait "$node"
done
$compose ps
