#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [[ "$#" -ne 3 ]]; then
  printf 'usage: monitor-staging.sh RUN_ID RUN_ATTEMPT WORKFLOW_SHA\n' >&2
  exit 2
fi

run_id="$1"
run_attempt="$2"
workflow_sha="$3"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
active_compose_file="$deploy_root/compose.yaml"
project="asteria-drive-staging"
expected_compose_sha256="d7d39a2e965849f364ceb25ab4106efd575f9a6d924e8ebfd9d508a594adc5dc"
expected_app_image="sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc"
expected_postgres_image="sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa"
expected_seaweedfs_image="sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
helper_image="chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
max_disk_used_percent=85
min_disk_available_kib=$((5 * 1024 * 1024))

[[ "$run_id" =~ ^[0-9]{1,20}$ ]] || { printf 'run id is invalid\n' >&2; exit 1; }
[[ "$run_attempt" =~ ^[0-9]{1,6}$ ]] || { printf 'run attempt is invalid\n' >&2; exit 1; }
[[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || { printf 'workflow sha is invalid\n' >&2; exit 1; }
[[ "$deploy_root" =~ ^/opt/asteria-drive/[A-Za-z0-9._-]+$ ]] || {
  printf 'deploy root is invalid\n' >&2
  exit 1
}

status="failed"
phase="initialize"
started_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
load1="0"
memory_used_percent="0"
root_filesystem="unknown"
disk_used_percent="0"
disk_available_kib="0"
docker_filesystem="unknown"
docker_disk_used_percent="0"
docker_disk_available_kib="0"
postgres_data_filesystem="unknown"
postgres_data_disk_used_percent="0"
postgres_data_disk_available_kib="0"
seaweedfs_data_filesystem="unknown"
seaweedfs_data_disk_used_percent="0"
seaweedfs_data_disk_available_kib="0"
docker_data_root_on_root_filesystem="false"
compose_verified="false"
api_container_verified="false"
postgres_container_verified="false"
seaweedfs_container_verified="false"
health_succeeded="false"
readiness_succeeded="false"
metrics_scrape_succeeded="false"
loopback_bindings_verified="false"
capacity_guard_verified="false"
data_volume_filesystems_verified="false"
captured_filesystem=""
captured_available_kib=""
captured_used_percent=""

parse_filesystem_output() {
  local output="$1" parsed extra
  parsed="$(printf '%s\n' "$output" | awk '
    NR == 1 { next }
    NR == 2 {
      gsub(/%/, "", $5)
      if (NF != 6 || $1 == "" || $4 !~ /^[0-9]+$/ || $5 !~ /^[0-9]+$/) exit 1
      print $1 "\t" $4 "\t" $5
      rows = 1
      next
    }
    NF > 0 { exit 1 }
    END { if (rows != 1) exit 1 }
  ')" || return 1
  IFS=$'\t' read -r captured_filesystem captured_available_kib captured_used_percent extra <<<"$parsed"
  [[ -n "$captured_filesystem" && -z "$extra" ]] || return 1
}

capture_host_filesystem() {
  local output
  output="$(df -Pk -- /)" || return 1
  parse_filesystem_output "$output"
}

capture_volume_filesystem() {
  local volume="$1" driver options output
  [[ "$volume" =~ ^asteria-drive-staging-[a-z0-9-]+$ ]] || return 1
  driver="$(docker volume inspect --format '{{.Driver}}' "$volume")" || return 1
  options="$(docker volume inspect --format '{{json .Options}}' "$volume")" || return 1
  [[ "$driver" == "local" ]] || return 1
  case "$options" in
    null|'{}') ;;
    *) return 1 ;;
  esac
  output="$(docker run --rm --pull never --network none --read-only \
    --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
    --memory 32m --cpus 0.25 --log-driver none \
    --volume "$volume:/capacity:ro" --entrypoint /bin/sh "$helper_image" \
    -c 'df -Pk /capacity')" || return 1
  parse_filesystem_output "$output"
}

verify_capacity() {
  local label="$1" used_percent="$2" available_kib="$3"
  if (( used_percent > max_disk_used_percent || available_kib < min_disk_available_kib )); then
    printf 'staging monitor capacity failure: %s is %s%% used with %s KiB available\n' \
      "$label" "$used_percent" "$available_kib" >&2
    return 1
  fi
}

verify_container() {
  local name="$1" expected_service="$2" expected_image="$3" expected_health="$4"
  local running image health project_label service_label
  running="$(docker inspect --format '{{.State.Running}}' "$name")" || return 1
  image="$(docker inspect --format '{{.Image}}' "$name")" || return 1
  [[ "$running" == "true" && "$image" == "$expected_image" ]] || return 1
  project_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$name")" || return 1
  service_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$name")" || return 1
  [[ "$project_label" == "$project" && "$service_label" == "$expected_service" ]] || return 1
  if [[ "$expected_health" != "none" ]]; then
    health="$(docker inspect --format '{{.State.Health.Status}}' "$name")" || return 1
    [[ "$health" == "$expected_health" ]] || return 1
  fi
}

verify_loopback_bindings() {
  local api_ports postgres_ports seaweedfs_ports
  api_ports="$(docker inspect --format '{{json .NetworkSettings.Ports}}' asteria-drive-staging-api-1)" || return 1
  postgres_ports="$(docker inspect --format '{{json .NetworkSettings.Ports}}' asteria-drive-staging-postgres-1)" || return 1
  seaweedfs_ports="$(docker inspect --format '{{json .NetworkSettings.Ports}}' asteria-drive-staging-seaweedfs-1)" || return 1
  API_PORTS="$api_ports" POSTGRES_PORTS="$postgres_ports" SEAWEEDFS_PORTS="$seaweedfs_ports" \
    python3 - <<'PY'
import json
import os

documents = {
    "api": json.loads(os.environ["API_PORTS"]),
    "postgres": json.loads(os.environ["POSTGRES_PORTS"]),
    "seaweedfs": json.loads(os.environ["SEAWEEDFS_PORTS"]),
}
actual = set()
for service, ports in documents.items():
    if not isinstance(ports, dict):
        raise SystemExit("container ports are malformed")
    for container_port, bindings in ports.items():
        for binding in bindings or []:
            if binding.get("HostIp") != "127.0.0.1":
                raise SystemExit("staging port is not loopback-bound")
            actual.add((service, container_port, binding.get("HostPort")))

expected = {
    ("api", "8080/tcp", "18080"),
    ("api", "9090/tcp", "19090"),
    ("seaweedfs", "8333/tcp", "18333"),
}
if actual != expected:
    raise SystemExit("staging port bindings differ from the reviewed set")
PY
}

write_evidence() {
  local completed_at
  completed_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')" || return 1
  E_STATUS="$status" E_PHASE="$phase" E_STARTED_AT="$started_at" E_COMPLETED_AT="$completed_at" \
  E_RUN_ID="$run_id" E_RUN_ATTEMPT="$run_attempt" E_WORKFLOW_SHA="$workflow_sha" \
  E_COMPOSE_SHA256="$expected_compose_sha256" E_EXPECTED_IMAGE="$expected_app_image" \
  E_LOAD1="$load1" E_MEMORY_USED="$memory_used_percent" \
  E_ROOT_FILESYSTEM="$root_filesystem" E_DISK_USED="$disk_used_percent" \
  E_DISK_AVAILABLE_KIB="$disk_available_kib" E_DOCKER_FILESYSTEM="$docker_filesystem" \
  E_DOCKER_DISK_USED="$docker_disk_used_percent" \
  E_DOCKER_DISK_AVAILABLE_KIB="$docker_disk_available_kib" \
  E_POSTGRES_FILESYSTEM="$postgres_data_filesystem" \
  E_POSTGRES_DISK_USED="$postgres_data_disk_used_percent" \
  E_POSTGRES_DISK_AVAILABLE_KIB="$postgres_data_disk_available_kib" \
  E_SEAWEEDFS_FILESYSTEM="$seaweedfs_data_filesystem" \
  E_SEAWEEDFS_DISK_USED="$seaweedfs_data_disk_used_percent" \
  E_SEAWEEDFS_DISK_AVAILABLE_KIB="$seaweedfs_data_disk_available_kib" \
  E_DOCKER_ON_ROOT="$docker_data_root_on_root_filesystem" \
  E_COMPOSE_VERIFIED="$compose_verified" E_API_VERIFIED="$api_container_verified" \
  E_POSTGRES_VERIFIED="$postgres_container_verified" \
  E_SEAWEEDFS_VERIFIED="$seaweedfs_container_verified" E_HEALTH="$health_succeeded" \
  E_READINESS="$readiness_succeeded" E_METRICS="$metrics_scrape_succeeded" \
  E_LOOPBACK="$loopback_bindings_verified" E_CAPACITY="$capacity_guard_verified" \
  E_DATA_VOLUMES="$data_volume_filesystems_verified" \
  E_MAX_DISK_USED="$max_disk_used_percent" E_MIN_DISK_AVAILABLE_KIB="$min_disk_available_kib" \
  python3 - <<'PY'
import json
import os

def boolean(name):
    value = os.environ[name]
    if value not in {"true", "false"}:
        raise SystemExit(f"invalid boolean for {name}")
    return value == "true"

def integer(name):
    value = int(os.environ[name])
    if value < 0:
        raise SystemExit(f"invalid integer for {name}")
    return value

report = {
    "schema": "asteria-drive-staging-monitor/v1",
    "status": os.environ["E_STATUS"],
    "last_phase": os.environ["E_PHASE"],
    "started_at": os.environ["E_STARTED_AT"],
    "completed_at": os.environ["E_COMPLETED_AT"],
    "github_run_id": os.environ["E_RUN_ID"],
    "github_run_attempt": os.environ["E_RUN_ATTEMPT"],
    "workflow_sha": os.environ["E_WORKFLOW_SHA"],
    "compose_sha256": os.environ["E_COMPOSE_SHA256"],
    "expected_image": os.environ["E_EXPECTED_IMAGE"],
    "claim_boundary": "staging-not-production",
    "load1": float(os.environ["E_LOAD1"]),
    "memory_used_percent": float(os.environ["E_MEMORY_USED"]),
    "root_filesystem": os.environ["E_ROOT_FILESYSTEM"],
    "disk_used_percent": integer("E_DISK_USED"),
    "disk_available_bytes": integer("E_DISK_AVAILABLE_KIB") * 1024,
    "docker_filesystem": os.environ["E_DOCKER_FILESYSTEM"],
    "docker_disk_used_percent": integer("E_DOCKER_DISK_USED"),
    "docker_disk_available_bytes": integer("E_DOCKER_DISK_AVAILABLE_KIB") * 1024,
    "postgres_data_filesystem": os.environ["E_POSTGRES_FILESYSTEM"],
    "postgres_data_disk_used_percent": integer("E_POSTGRES_DISK_USED"),
    "postgres_data_disk_available_bytes": integer("E_POSTGRES_DISK_AVAILABLE_KIB") * 1024,
    "seaweedfs_data_filesystem": os.environ["E_SEAWEEDFS_FILESYSTEM"],
    "seaweedfs_data_disk_used_percent": integer("E_SEAWEEDFS_DISK_USED"),
    "seaweedfs_data_disk_available_bytes": integer("E_SEAWEEDFS_DISK_AVAILABLE_KIB") * 1024,
    "docker_data_root_on_root_filesystem": boolean("E_DOCKER_ON_ROOT"),
    "compose_verified": boolean("E_COMPOSE_VERIFIED"),
    "api_container_verified": boolean("E_API_VERIFIED"),
    "postgres_container_verified": boolean("E_POSTGRES_VERIFIED"),
    "seaweedfs_container_verified": boolean("E_SEAWEEDFS_VERIFIED"),
    "health_succeeded": boolean("E_HEALTH"),
    "readiness_succeeded": boolean("E_READINESS"),
    "metrics_scrape_succeeded": boolean("E_METRICS"),
    "loopback_bindings_verified": boolean("E_LOOPBACK"),
    "capacity_guard_verified": boolean("E_CAPACITY"),
    "data_volume_filesystems_verified": boolean("E_DATA_VOLUMES"),
    "capacity_max_disk_used_percent": integer("E_MAX_DISK_USED"),
    "capacity_min_disk_available_bytes": integer("E_MIN_DISK_AVAILABLE_KIB") * 1024,
}
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

finish() {
  local code="$?"
  trap - EXIT
  if ! write_evidence; then
    printf 'staging monitor could not write evidence\n' >&2
    exit 1
  fi
  exit "$code"
}
trap finish EXIT

phase="host-capacity"
load1="$(awk '{print $1}' /proc/loadavg)"
memory_used_percent="$(awk '/MemTotal:/ {t=$2} /MemAvailable:/ {a=$2} END {printf "%.2f", (t-a)*100/t}' /proc/meminfo)"
capture_host_filesystem
root_filesystem="$captured_filesystem"
disk_available_kib="$captured_available_kib"
disk_used_percent="$captured_used_percent"
verify_capacity "root filesystem" "$disk_used_percent" "$disk_available_kib"

phase="compose-identity"
[[ -f "$active_compose_file" && ! -L "$active_compose_file" ]] || exit 1
[[ "$(sha256sum "$active_compose_file" | awk '{print $1}')" == "$expected_compose_sha256" ]] || exit 1
compose_verified="true"

phase="docker-capacity"
capture_volume_filesystem "asteria-drive-staging-app-secrets"
docker_filesystem="$captured_filesystem"
docker_disk_available_kib="$captured_available_kib"
docker_disk_used_percent="$captured_used_percent"
verify_capacity "Docker data filesystem" "$docker_disk_used_percent" "$docker_disk_available_kib"
[[ "$docker_filesystem" == "$root_filesystem" ]] && docker_data_root_on_root_filesystem="true"

phase="data-volume-capacity"
for volume in asteria-drive-staging-postgres-data asteria-drive-staging-seaweedfs-data; do
  [[ "$(docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}' "$volume")" == "$project" ]] || exit 1
  capture_volume_filesystem "$volume"
  [[ "$captured_filesystem" == "$docker_filesystem" ]] || exit 1
  verify_capacity "$volume" "$captured_used_percent" "$captured_available_kib"
  case "$volume" in
    asteria-drive-staging-postgres-data)
      postgres_data_filesystem="$captured_filesystem"
      postgres_data_disk_available_kib="$captured_available_kib"
      postgres_data_disk_used_percent="$captured_used_percent"
      ;;
    asteria-drive-staging-seaweedfs-data)
      seaweedfs_data_filesystem="$captured_filesystem"
      seaweedfs_data_disk_available_kib="$captured_available_kib"
      seaweedfs_data_disk_used_percent="$captured_used_percent"
      ;;
  esac
done
data_volume_filesystems_verified="true"
capacity_guard_verified="true"

phase="container-identity"
verify_container asteria-drive-staging-api-1 api "$expected_app_image" none
api_container_verified="true"
verify_container asteria-drive-staging-postgres-1 postgres "$expected_postgres_image" healthy
postgres_container_verified="true"
verify_container asteria-drive-staging-seaweedfs-1 seaweedfs "$expected_seaweedfs_image" healthy
seaweedfs_container_verified="true"

phase="loopback-bindings"
verify_loopback_bindings
loopback_bindings_verified="true"

phase="http-probes"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/healthz >/dev/null
health_succeeded="true"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/readyz >/dev/null
readiness_succeeded="true"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:19090/metrics |
  awk '/^asteria_http_requests_total([ {]|$)/ { found = 1 } END { exit(found ? 0 : 1) }'
metrics_scrape_succeeded="true"

phase="complete"
status="success"
