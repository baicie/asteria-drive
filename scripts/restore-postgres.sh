#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s ARCHIVE.dump\n' "$0" >&2
  exit 2
fi
: "${PGSERVICEFILE:?PGSERVICEFILE must name a mounted libpq service file}"
: "${PGSERVICE:?PGSERVICE must name the isolated restore service}"
: "${ASTERIA_RESTORE_TARGET_KIND:?ASTERIA_RESTORE_TARGET_KIND must be isolated}"
: "${ASTERIA_RESTORE_CONFIRM:?ASTERIA_RESTORE_CONFIRM must be isolated-target}"
if [[ "$ASTERIA_RESTORE_TARGET_KIND" != "isolated" || "$ASTERIA_RESTORE_CONFIRM" != "isolated-target" ]]; then
  printf 'restore refused: target is not explicitly confirmed as isolated\n' >&2
  exit 2
fi
if [[ "$PGSERVICE" != asteria_restore_* ]]; then
  printf 'restore refused: PGSERVICE must begin with asteria_restore_\n' >&2
  exit 2
fi
if [[ ! "$PGSERVICE" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  printf 'PGSERVICE contains unsupported characters\n' >&2
  exit 2
fi
if [[ ! -r "$PGSERVICEFILE" ]]; then
  printf 'PGSERVICEFILE is not readable\n' >&2
  exit 2
fi
service_mode="$(stat -c '%a' "$PGSERVICEFILE" 2>/dev/null || true)"
if [[ "$service_mode" != "600" ]]; then
  printf 'PGSERVICEFILE must have mode 0600\n' >&2
  exit 2
fi
archive="$1"
if [[ ! -f "$archive" || ! -f "$archive.sha256" ]]; then
  printf 'archive and adjacent .sha256 file are required\n' >&2
  exit 2
fi
command -v pg_restore >/dev/null
command -v sha256sum >/dev/null
connection="service=$PGSERVICE sslmode=verify-full"

(
  cd "$(dirname "$archive")"
  sha256sum -c "$(basename "$archive").sha256"
)
pg_restore --list "$archive" >/dev/null
pg_restore \
  --dbname="$connection" \
  --clean \
  --if-exists \
  --no-owner \
  --no-acl \
  --exit-on-error \
  --single-transaction \
  "$archive"
