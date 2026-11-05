#!/usr/bin/env bash
# Phase S3 read-only staging forensics.
#
# Runs ONLY read-only inspection on the staging host and prints a report whose
# values are verdicts (MATCH / MISMATCH / MISSING / UNKNOWN) plus non-secret
# numbers (capacity, image identity, container health). It never writes,
# restarts, prunes, pulls or execs into anything, and it never prints file
# digests, configuration bodies, environment variables, mount options or
# secret material.
#
# Usage (as root on the staging host):
#   ./s3-staging-readonly-evidence.sh                 # capacity + identity + containers
#   ./s3-staging-readonly-evidence.sh --with-du       # also measure directory usage (I/O heavy)
#
# Reviewed plan: docs/operations/evidence/phase-s3-staging-host-evidence.md
set -Eeuo pipefail
umask 077

with_du="false"
for arg in "$@"; do
  case "$arg" in
    --with-du) with_du="true" ;;
    -h | --help) sed -n '2,16p' "$0"; exit 0 ;;
    *) printf 'unknown argument\n' >&2; exit 2 ;;
  esac
done

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  printf 'this script must run as root (read-only, but /usr/local/libexec and /etc/asteria-drive are root-owned)\n' >&2
  exit 1
fi

# Expected digests, quoted from the repository bundles. They are compared
# locally; only the verdict is printed, never the file's actual digest.
expected_monitor="d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69"
expected_monitor_v011="3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279"
expected_recovery="91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4"
expected_recovery_v011="3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a"
expected_controller="754ba6711d8e7b67a4dc358d5f14f31099976f10cbd74d4d0e17bc808491252c"
expected_verifier="77e537f7cfc8824c8558b72466161065770b4488110458c84e0246dadcf422d7"
expected_compose_v020="cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005"
expected_compose_v011="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"

max_disk_used_percent=85
min_disk_available_kib=$((5 * 1024 * 1024))

say() { printf '%s\n' "$*"; }
rule() { printf '%s\n' "------------------------------------------------------------"; }

# compare <path> <expected> -> MATCH / MISMATCH / MISSING
compare() {
  local path="$1" expected="$2" actual
  if [[ ! -f "$path" || -L "$path" ]]; then
    printf 'MISSING'
    return
  fi
  actual="$(sha256sum -- "$path" 2>/dev/null | awk '{print $1}')" || actual=""
  if [[ -z "$actual" ]]; then
    printf 'UNKNOWN'
  elif [[ "$actual" == "$expected" ]]; then
    printf 'MATCH'
  else
    printf 'MISMATCH'
  fi
}

# compare_any <path> <expected> [<expected>...] -> MATCH / MISMATCH / MISSING
compare_any() {
  local path="$1" actual candidate
  shift
  if [[ ! -f "$path" || -L "$path" ]]; then
    printf 'MISSING'
    return
  fi
  actual="$(sha256sum -- "$path" 2>/dev/null | awk '{print $1}')" || actual=""
  [[ -n "$actual" ]] || { printf 'UNKNOWN'; return; }
  for candidate in "$@"; do
    [[ "$actual" == "$candidate" ]] && { printf 'MATCH'; return; }
  done
  printf 'MISMATCH'
}

parse_df() {
  # Prints "<available_kib>\t<used_percent>" — the same field order the
  # production monitor uses (df -Pk: $4 Available, $5 Capacity).
  awk '
    NR == 1 { next }
    NR == 2 {
      gsub(/%/, "", $5)
      if (NF != 6 || $1 == "" || $4 !~ /^[0-9]+$/ || $5 !~ /^[0-9]+$/) exit 1
      print $4 "\t" $5
      rows = 1
      next
    }
    NF > 0 { exit 1 }
    END { if (rows != 1) exit 1 }
  '
}

say "# Phase S3 read-only staging forensics"
say "# host: $(hostname 2>/dev/null || echo unknown)"
say "# time: $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
say "# note: verdicts only; no digest, config body, env or secret is printed"

rule
say "## P1 capacity"
rule
if root_row="$(df -Pk -- / 2>/dev/null | parse_df)" && [[ -n "$root_row" ]]; then
  IFS=$'\t' read -r disk_available_kib disk_used_percent <<<"$root_row"
  say "root_filesystem_used_percent: $disk_used_percent"
  say "root_filesystem_available_kib: $disk_available_kib"
  say "root_filesystem_available_gib: $(awk -v k="$disk_available_kib" 'BEGIN{printf "%.2f", k/1048576}')"
  if (( disk_used_percent > max_disk_used_percent )); then
    say "capacity_gate: BLOCK (used percent above $max_disk_used_percent)"
  elif (( disk_available_kib < min_disk_available_kib )); then
    say "capacity_gate: BLOCK (available below $min_disk_available_kib KiB)"
  else
    say "capacity_gate: PASS"
  fi
else
  say "root_filesystem_used_percent: UNKNOWN (df output not parseable)"
  say "root_filesystem_available_kib: UNKNOWN"
  say "capacity_gate: UNKNOWN"
fi

if type_row="$(df -PTk -- / 2>/dev/null | awk 'NR==2{print $2}')" && [[ -n "$type_row" ]]; then
  say "root_filesystem_type: $type_row"
else
  say "root_filesystem_type: UNKNOWN"
fi

if inode_row="$(df -Pi -- / 2>/dev/null | awk 'NR==2{gsub(/%/,"",$5); print $5}')" && [[ -n "$inode_row" ]]; then
  say "root_filesystem_inode_used_percent: $inode_row"
else
  say "root_filesystem_inode_used_percent: UNKNOWN"
fi

say "load1: $(awk '{print $1}' /proc/loadavg 2>/dev/null || echo UNKNOWN)"
say "memory_used_percent: $(awk '/MemTotal:/{t=$2} /MemAvailable:/{a=$2} END{if (t>0 && a!="") printf "%.2f", (t-a)*100/t; else print "UNKNOWN"}' /proc/meminfo 2>/dev/null || echo UNKNOWN)"

if command -v docker >/dev/null 2>&1; then
  say "docker_system_df:"
  docker system df 2>/dev/null | sed -e 's/^/  /' || say "  UNKNOWN"
else
  say "docker_system_df: UNKNOWN (docker not found)"
fi

say "journal_disk_usage: $(journalctl --disk-usage 2>/dev/null | tr -d '\n' || echo UNKNOWN)"

if [[ "$with_du" == "true" ]]; then
  say "du_top_level (may take a few minutes; -x does not cross filesystems):"
  timeout 300 du -x -d1 -h / 2>/dev/null | sort -h | tail -20 | sed -e 's/^/  /' || say "  UNKNOWN (timed out or failed)"
else
  say "du_top_level: NOT_RUN (rerun with --with-du inside the maintenance window)"
fi

rule
say "## P2 dispatcher identity and accept-set"
rule
say "dispatch_file:            $(compare_any /usr/local/libexec/asteria-staging-dispatch "$expected_monitor" "$expected_monitor_v011" "$expected_recovery" "$expected_recovery_v011")"
say "monitor_file:             $(compare /usr/local/libexec/asteria-staging-monitor "$expected_monitor")"
say "monitor_v011_file:        $(compare /usr/local/libexec/asteria-staging-monitor-v0.1.1 "$expected_monitor_v011")"
say "recovery_file:            $(compare /usr/local/libexec/asteria-staging-recovery "$expected_recovery")"
say "recovery_v011_file:       $(compare /usr/local/libexec/asteria-staging-recovery-v0.1.1 "$expected_recovery_v011")"
say "controller_file:          $(compare /usr/local/libexec/asteria-staging-controller "$expected_controller")"
say "verifier_file:            $(compare /usr/local/libexec/staging_runtime_verifier.py "$expected_verifier")"

# On a v0.1.1-only host the single monitor slot legitimately holds the v0.1.1
# monitor, so report that explicitly instead of a bare MISMATCH.
if [[ -f /usr/local/libexec/asteria-staging-monitor && ! -L /usr/local/libexec/asteria-staging-monitor ]]; then
  slot="$(compare /usr/local/libexec/asteria-staging-monitor "$expected_monitor")"
  slot_v011="$(compare /usr/local/libexec/asteria-staging-monitor "$expected_monitor_v011")"
  if [[ "$slot" != "MATCH" && "$slot_v011" == "MATCH" ]]; then
    say "monitor_slot_generation:  v0.1.1 (single-slot anchor, not a drift)"
  elif [[ "$slot" == "MATCH" ]]; then
    say "monitor_slot_generation:  v0.2.0"
  else
    say "monitor_slot_generation:  UNKNOWN (neither v0.1.1 nor v0.2.0)"
  fi
fi

if [[ -f /opt/asteria-drive/staging/compose.yaml && ! -L /opt/asteria-drive/staging/compose.yaml ]]; then
  say "active_compose:           v0.2.0=$(compare /opt/asteria-drive/staging/compose.yaml "$expected_compose_v020") v0.1.1=$(compare /opt/asteria-drive/staging/compose.yaml "$expected_compose_v011")"
else
  say "active_compose:           MISSING"
fi

config="/etc/asteria-drive/staging-dispatch.conf"
if [[ -f "$config" && ! -L "$config" ]]; then
  keys="$(grep -oE '^[A-Za-z0-9_]+=' "$config" 2>/dev/null | tr -d '=' | sort -u | tr '\n' ',' || true)"
  say "dispatch_conf_keys:       ${keys:-UNKNOWN}"
  # The accept-set is a set of digests; compare each key's value locally and
  # print only which script it designates.
  value_of() { grep -E "^$1=" "$config" 2>/dev/null | head -1 | sed -E "s/^$1=//; s/^'//; s/'$//" || true; }
  for key in monitor_script_sha256 monitor_v011_script_sha256 recovery_script_sha256 recovery_v011_script_sha256 controller_sha256 verifier_sha256; do
    value="$(value_of "$key")"
    if [[ -z "$value" ]]; then
      say "conf_${key}: ABSENT"
      continue
    fi
    verdict="OTHER"
    case "$key:$value" in
      monitor_script_sha256:"$expected_monitor") verdict="v0.2.0 monitor" ;;
      monitor_script_sha256:"$expected_monitor_v011") verdict="v0.1.1 monitor (single-slot generation)" ;;
      monitor_v011_script_sha256:"$expected_monitor_v011") verdict="v0.1.1 monitor" ;;
      monitor_v011_script_sha256:"$expected_monitor") verdict="v0.2.0 monitor (wrong key)" ;;
      recovery_script_sha256:"$expected_recovery") verdict="v0.2.0 recovery" ;;
      recovery_script_sha256:"$expected_recovery_v011") verdict="v0.1.1 recovery (single-slot generation)" ;;
      recovery_v011_script_sha256:"$expected_recovery_v011") verdict="v0.1.1 recovery" ;;
      recovery_v011_script_sha256:"$expected_recovery") verdict="v0.2.0 recovery (wrong key)" ;;
      controller_sha256:"$expected_controller") verdict="controller" ;;
      verifier_sha256:"$expected_verifier") verdict="verifier" ;;
    esac
    say "conf_${key}: $verdict"
  done
else
  say "dispatch_conf_keys: MISSING"
fi

if [[ -f /etc/asteria-drive/staging-releases.json && ! -L /etc/asteria-drive/staging-releases.json ]]; then
  releases_py=""
  for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'pass' >/dev/null 2>&1; then
      releases_py="$candidate"
      break
    fi
  done
  if [[ -z "$releases_py" ]]; then
    say "releases_json: PRESENT (UNKNOWN content: no working python interpreter)"
  elif releases_out="$("$releases_py" -c '
import json, sys
try:
    data = json.load(open(sys.argv[1], encoding="utf-8"))
except Exception:
    raise SystemExit(1)
if not isinstance(data, dict):
    raise SystemExit(1)
versions = ",".join(sorted(data))
schemas = ",".join(sorted({str(v.get("schema_version")) for v in data.values() if isinstance(v, dict)}))
print("present versions=" + versions + " schema_versions=" + schemas)
' /etc/asteria-drive/staging-releases.json 2>/dev/null)" && [[ -n "$releases_out" ]]; then
    say "releases_json: $releases_out"
  else
    say "releases_json: PRESENT (UNKNOWN content: unreadable or not the expected JSON object)"
  fi
else
  say "releases_json: MISSING"
fi

say "ownership:"
for path in \
  /usr/local/libexec/asteria-staging-dispatch \
  /usr/local/libexec/asteria-staging-monitor \
  /usr/local/libexec/asteria-staging-monitor-v0.1.1 \
  /usr/local/libexec/asteria-staging-recovery \
  /usr/local/libexec/asteria-staging-recovery-v0.1.1 \
  /usr/local/libexec/asteria-staging-controller \
  /usr/local/libexec/staging_runtime_verifier.py \
  /etc/asteria-drive/staging-dispatch.conf \
  /etc/asteria-drive/staging-releases.json \
  /run/asteria-drive/staging.lock; do
  if [[ -e "$path" ]]; then
    say "  $(stat -c '%U:%G %a' "$path" 2>/dev/null || echo UNKNOWN) $path"
  else
    say "  MISSING $path"
  fi
done

if [[ -f /opt/asteria-drive/staging/controller-state.json && ! -L /opt/asteria-drive/staging/controller-state.json ]]; then
  say "controller_state: PRESENT (read the 'state' field with: python3 -c \"import json;print(json.load(open('/opt/asteria-drive/staging/controller-state.json'))['state'])\")"
else
  say "controller_state: MISSING"
fi

rule
say "## P3 running images and health"
rule
if command -v docker >/dev/null 2>&1; then
  docker ps --filter name=asteria-drive-staging --format 'ps: {{.Names}} | {{.Image}} | {{.Status}}' 2>/dev/null || say "ps: UNKNOWN"
  for name in asteria-drive-staging-api-1 asteria-drive-staging-postgres-1 asteria-drive-staging-seaweedfs-1; do
    row="$(docker inspect --format '{{.Name}} | {{.Config.Image}} | {{.Image}} | {{.State.Status}} | {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null)" || row="MISSING"
    case "$row" in
      *"@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"*) version="v0.1.1" ;;
      *"@sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853"*) version="v0.2.0" ;;
      MISSING*) version="MISSING" ;;
      *) version="UNKNOWN (tag or local reference; not a pinned digest)" ;;
    esac
    say "inspect: $row"
    say "  version: $version"
  done
  say "volumes:"
  docker volume ls --filter name=asteria-drive-staging --format '  {{.Name}} {{.Driver}}' 2>/dev/null || say "  UNKNOWN"
else
  say "containers: UNKNOWN (docker not found)"
fi

rule
say "## done"
say "Record this output in docs/operations/evidence/phase-s3-staging-host-evidence-<date>.md."
say "Do not paste it into an issue, PR or workflow artifact."
