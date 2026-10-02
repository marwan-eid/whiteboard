#!/bin/sh
# Updates the deployed stack to the latest images and configuration.
# Run on the server, in /opt/whiteboard:   sh deploy/update.sh [image tag]
# The tag defaults to IMAGE_TAG in .env (normally "latest"). Each release
# workflow run also publishes the commit SHA as a tag, for pinning or rollback.
# /etc/whiteboard.conf (written at first boot) says which compose files and
# services this machine runs; the small fallback machine runs fewer.
set -eu
cd "$(dirname "$0")/.."
COMPOSE_FILES="-f compose.yaml -f deploy/compose.prod.yaml"
SERVICES=""
[ -f /etc/whiteboard.conf ] && . /etc/whiteboard.conf
git pull --ff-only
if [ -n "${1:-}" ]; then
  sed -i "s/^IMAGE_TAG=.*/IMAGE_TAG=$1/" .env
fi
compose="docker compose $COMPOSE_FILES"
$compose pull $SERVICES
if [ -n "$SERVICES" ]; then
  $compose up -d $SERVICES
else
  # Nodes restart one at a time: clients of the restarting node move to the
  # other one (W8), so boards stay available.
  $compose up -d --no-deps postgres prometheus grafana backup web
  for node in node-1 node-2; do
    $compose up -d --no-deps --wait "$node"
  done
fi
$compose ps
