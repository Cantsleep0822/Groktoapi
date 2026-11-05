#!/bin/sh
set -eu

umask 077

quality_guard_dir=/var/lib/grok2api-quality-guard
mkdir -p "${quality_guard_dir}"
chown grok2api:grok2api "${quality_guard_dir}"
chmod 0700 "${quality_guard_dir}"

config_source="${GROK2API_CONFIG_SOURCE:-/run/grok2api/config.yaml}"
# Render Secret Files are mounted here, outside the image and source archive.
if [ "$config_source" = /run/grok2api/config.yaml ] && [ ! -f "$config_source" ] && [ -f /etc/secrets/config.yaml ]; then
  config_source=/etc/secrets/config.yaml
fi
if [ ! -f "$config_source" ]; then
  echo "missing config: $config_source" >&2
  echo "provide /etc/secrets/config.yaml on Render or set GROK2API_CONFIG_SOURCE" >&2
  exit 1
fi
cp "$config_source" /app/config.yaml
chown grok2api:grok2api /app/config.yaml
chmod 0600 /app/config.yaml

# Honor Render's PORT only for the image's default command.
if [ "$*" = "/app/grok2api --config /app/config.yaml --listen 0.0.0.0:8000" ]; then
  set -- /app/grok2api --config /app/config.yaml --listen "0.0.0.0:${PORT:-8000}"
fi
case "${GROK2API_CF_ACCESS_ENABLED:-false}" in
  true|1) set -- /usr/local/bin/cloudflared-supervisor client -- "$@" ;;
  false|0|"") ;;
  *) echo "GROK2API_CF_ACCESS_ENABLED must be true or false" >&2; exit 1 ;;
esac

exec su-exec grok2api:grok2api "$@"
