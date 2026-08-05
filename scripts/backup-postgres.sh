#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'usage: %s OUTPUT_DIRECTORY\n' "$0" >&2
  exit 2
fi
: "${PGSERVICEFILE:?PGSERVICEFILE must name a mounted libpq service file}"
: "${PGSERVICE:?PGSERVICE must name the read-only backup service}"
if [[ ! -r "$PGSERVICEFILE" ]]; then
  printf 'PGSERVICEFILE is not readable\n' >&2
  exit 2
fi
if [[ ! "$PGSERVICE" =~ ^[A-Za-z0-9_.-]+$ ]]; then
  printf 'PGSERVICE contains unsupported characters\n' >&2
  exit 2
fi
service_mode="$(stat -c '%a' "$PGSERVICEFILE" 2>/dev/null || true)"
if [[ "$service_mode" != "600" ]]; then
  printf 'PGSERVICEFILE must have mode 0600\n' >&2
  exit 2
fi
command -v pg_dump >/dev/null
command -v pg_restore >/dev/null
command -v sha256sum >/dev/null

umask 077
destination="$1"
mkdir -p -- "$destination"
destination="$(cd "$destination" && pwd -P)"
timestamp="$(date -u +'%Y%m%dT%H%M%SZ')"
archive="$destination/asteria_${timestamp}.dump"
if [[ -e "$archive" || -e "$archive.sha256" ]]; then
  printf 'backup destination already contains %s\n' "$(basename "$archive")" >&2
  exit 2
fi
partial="$(mktemp "$destination/.asteria_${timestamp}.partial.XXXXXX")"
trap 'rm -f -- "$partial"' EXIT

pg_dump \
  --dbname="service=$PGSERVICE" \
  --format=custom \
  --compress=9 \
  --no-owner \
  --no-acl \
  --file="$partial"
pg_restore --list "$partial" >/dev/null
mv -- "$partial" "$archive"
trap - EXIT
(
  cd "$destination"
  sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256"
)
printf '%s\n' "$archive"
