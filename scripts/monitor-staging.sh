#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [[ "$#" -ne 4 ]]; then
  printf 'usage: monitor-staging.sh RUN_ID RUN_ATTEMPT WORKFLOW_SHA MONITOR_SCRIPT_SHA256\n' >&2
  exit 2
fi

run_id="$1"
run_attempt="$2"
workflow_sha="$3"
monitor_script_sha256="$4"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
active_compose_file="$deploy_root/compose.yaml"
project="asteria-drive-staging"
expected_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"
expected_app_image="sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"
expected_postgres_image="sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa"
expected_seaweedfs_image="sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
app_image="ghcr.io/baicie/asteria-drive@$expected_app_image"
postgres_image="postgres:17.5-alpine@$expected_postgres_image"
seaweedfs_image="chrislusf/seaweedfs:3.85@$expected_seaweedfs_image"
helper_image="chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
max_disk_used_percent=85
min_disk_available_kib=$((5 * 1024 * 1024))

[[ "$run_id" =~ ^[0-9]{1,20}$ ]] || { printf 'run id is invalid\n' >&2; exit 1; }
[[ "$run_attempt" =~ ^[0-9]{1,6}$ ]] || { printf 'run attempt is invalid\n' >&2; exit 1; }
[[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || { printf 'workflow sha is invalid\n' >&2; exit 1; }
[[ "$monitor_script_sha256" =~ ^[0-9a-f]{64}$ ]] || { printf 'monitor script sha is invalid\n' >&2; exit 1; }
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
postgres_tls_verified="false"
postgres_tls_dsn_verified="false"
postgres_hba_verified="false"
postgres_plaintext_rejected="false"
postgres_plaintext_stderr_present="false"
postgres_plaintext_stderr_sha256="none"
postgres_scram_password_verified="false"
postgres_tls_connections=0
postgres_tls_version="none"
postgres_tls_cipher="none"
postgres_tls_bits=0
postgres_tls_ca_sha256="none"
postgres_tls_leaf_sha256="none"
postgres_tls_leaf_san="none"
postgres_tls_leaf_not_after="none"
tls_temp_dir=""
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
  local name="$1" expected_service="$2" expected_ref="$3" expected_health="$4"
  local running image image_ref expected_id health project_label service_label
  running="$(docker inspect --format '{{.State.Running}}' "$name")" || return 1
  image="$(docker inspect --format '{{.Image}}' "$name")" || return 1
  image_ref="$(docker inspect --format '{{.Config.Image}}' "$name")" || return 1
  expected_id="$(docker image inspect --format '{{.Id}}' "$expected_ref")" || return 1
  [[ "$running" == "true" && "$image_ref" == "$expected_ref" && "$image" == "$expected_id" ]] || return 1
  project_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$name")" || return 1
  service_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$name")" || return 1
  [[ "$project_label" == "$project" && "$service_label" == "$expected_service" ]] || return 1
  if [[ "$expected_health" != "none" ]]; then
    health="$(docker inspect --format '{{.State.Health.Status}}' "$name")" || return 1
    [[ "$health" == "$expected_health" ]] || return 1
  fi
}

verify_postgres_dsn() {
  docker run --rm --pull never --network none --read-only --user root \
    --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
    --memory 32m --cpus 0.25 --log-driver none \
    --mount type=volume,source=asteria-drive-staging-app-secrets,target=/app-secrets,readonly \
    --mount type=volume,source=asteria-drive-staging-postgres-secrets,target=/postgres-secrets,readonly \
    --entrypoint /bin/sh "$helper_image" -ec '
      password="$(cat /postgres-secrets/postgres-password)"
      dsn="$(cat /app-secrets/database-url-tls)"
      expected="postgres://asteria:${password}@postgres:5432/asteria?sslmode=verify-full&sslrootcert=/var/run/secrets/asteria/postgres-ca.crt&application_name=asteria-drive-staging-api"
      [ "$dsn" = "$expected" ]
    '
  postgres_tls_dsn_verified="true"
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

verify_postgres_tls() {
  local ca_file leaf_file settings hba_rules tls_row tls_all plaintext_stderr plaintext_exit not_after_raw
  tls_temp_dir="$(mktemp -d /tmp/asteria-staging-monitor-tls.XXXXXXXX)"
  [[ "$tls_temp_dir" =~ ^/tmp/asteria-staging-monitor-tls\.[A-Za-z0-9]{8}$ ]] || return 1
  ca_file="$tls_temp_dir/postgres-ca.crt"
  leaf_file="$tls_temp_dir/postgres-server.crt"
  docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    cat /var/run/secrets/asteria/postgres-ca.crt >"$ca_file" || return 1
  docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    cat /var/run/secrets/asteria/postgres-server.crt >"$leaf_file" || return 1
  [[ -s "$ca_file" && -s "$leaf_file" ]] || return 1
  openssl verify -CAfile "$ca_file" -verify_hostname postgres "$leaf_file" >/dev/null || return 1
  openssl x509 -in "$leaf_file" -checkend $((30 * 24 * 60 * 60)) -noout >/dev/null || return 1
  postgres_tls_ca_sha256="$(openssl x509 -in "$ca_file" -outform DER | sha256sum | awk '{print $1}')"
  postgres_tls_leaf_sha256="$(openssl x509 -in "$leaf_file" -outform DER | sha256sum | awk '{print $1}')"
  [[ "$postgres_tls_ca_sha256" =~ ^[0-9a-f]{64}$ && "$postgres_tls_leaf_sha256" =~ ^[0-9a-f]{64}$ ]] || return 1
  postgres_tls_leaf_san="$(openssl x509 -in "$leaf_file" -noout -ext subjectAltName |
    awk 'NR > 1 { gsub(/[[:space:]]/, ""); printf "%s", $0 }')"
  [[ "$postgres_tls_leaf_san" == "DNS:postgres" ]] || return 1
  not_after_raw="$(openssl x509 -in "$leaf_file" -noout -enddate | cut -d= -f2-)"
  postgres_tls_leaf_not_after="$(date -u -d "$not_after_raw" +'%Y-%m-%dT%H:%M:%SZ')" || return 1

  verify_postgres_dsn
  settings="$(docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    psql -U asteria -d asteria -AtF $'\t' -c \
      "SELECT current_setting('ssl'), current_setting('ssl_min_protocol_version'), current_setting('hba_file')")" || return 1
  [[ "$settings" == $'on\tTLSv1.2\t/var/run/secrets/asteria/pg_hba.conf' ]] || return 1
  hba_rules="$(docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    psql -U asteria -d asteria -Atqc \
      "SELECT COALESCE(string_agg(format('%s|%s|%s|%s|%s|%s|%s', type, array_to_string(database, ','), array_to_string(user_name, ','), COALESCE(address, ''), COALESCE(netmask, ''), auth_method, COALESCE(error, '')), E'\\n' ORDER BY line_number), '') FROM pg_hba_file_rules")" || return 1
  [[ "$hba_rules" == $'local|all|all|||trust|\nhostnossl|all|all|0.0.0.0|0.0.0.0|reject|\nhostnossl|all|all|::|::|reject|\nhostssl|all|all|0.0.0.0|0.0.0.0|scram-sha-256|\nhostssl|all|all|::|::|scram-sha-256|' ]] || return 1
  postgres_hba_verified="true"
  tls_row="$(docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    psql -U asteria -d asteria -AtF $'\t' -c \
      "SELECT count(*)::text, COALESCE(bool_and(s.ssl), false)::text, COALESCE(min(s.version), ''), COALESCE(min(s.cipher), ''), COALESCE(min(s.bits), 0)::text FROM pg_stat_activity a JOIN pg_stat_ssl s USING (pid) WHERE a.application_name = 'asteria-drive-staging-api'")" || return 1
  IFS=$'\t' read -r postgres_tls_connections tls_all postgres_tls_version postgres_tls_cipher postgres_tls_bits <<<"$tls_row"
  [[ "$postgres_tls_connections" =~ ^[0-9]+$ && "$postgres_tls_connections" -ge 1 ]] || return 1
  [[ "$tls_all" == "true" && "$postgres_tls_version" =~ ^TLSv1\.[23]$ ]] || return 1
  [[ "$postgres_tls_cipher" =~ ^[A-Za-z0-9_-]+$ ]] || return 1
  [[ "$postgres_tls_bits" =~ ^[0-9]+$ && "$postgres_tls_bits" -ge 128 ]] || return 1
  [[ "$(docker compose -p "$project" -f "$active_compose_file" exec -T postgres \
    psql -U asteria -d asteria -Atqc \
      "SELECT COALESCE((SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname = 'asteria'), false)")" == "t" ]] || return 1
  postgres_scram_password_verified="true"

  plaintext_stderr="$tls_temp_dir/postgres-plaintext.stderr"
  set +e
  docker compose -p "$project" -f "$active_compose_file" exec -T --user root postgres sh -ec '
    export PGPASSWORD="$(cat /var/run/secrets/asteria/postgres-password)"
    exec psql "host=postgres port=5432 dbname=asteria user=asteria sslmode=disable connect_timeout=5" -Atqc "SELECT 1"
  ' >/dev/null 2>"$plaintext_stderr"
  plaintext_exit="$?"
  set -e
  [[ "$plaintext_exit" -ne 0 && -s "$plaintext_stderr" ]] || return 1
  postgres_plaintext_stderr_present="true"
  postgres_plaintext_stderr_sha256="$(sha256sum "$plaintext_stderr" | awk '{print $1}')"
  [[ "$postgres_plaintext_stderr_sha256" =~ ^[0-9a-f]{64}$ ]] || return 1
  rm -f -- "$plaintext_stderr" || return 1
  postgres_plaintext_rejected="true"
  postgres_tls_verified="true"
}

cleanup_tls_temp() {
  [[ -n "$tls_temp_dir" ]] || return 0
  [[ "$tls_temp_dir" =~ ^/tmp/asteria-staging-monitor-tls\.[A-Za-z0-9]{8}$ ]] || return 1
  rm -rf -- "$tls_temp_dir"
  tls_temp_dir=""
}

write_evidence() {
  local completed_at
  completed_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')" || return 1
  E_STATUS="$status" E_PHASE="$phase" E_STARTED_AT="$started_at" E_COMPLETED_AT="$completed_at" \
  E_RUN_ID="$run_id" E_RUN_ATTEMPT="$run_attempt" E_WORKFLOW_SHA="$workflow_sha" \
  E_MONITOR_SCRIPT_SHA256="$monitor_script_sha256" \
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
  E_POSTGRES_TLS_VERIFIED="$postgres_tls_verified" \
  E_POSTGRES_TLS_DSN_VERIFIED="$postgres_tls_dsn_verified" \
  E_POSTGRES_HBA_VERIFIED="$postgres_hba_verified" \
  E_POSTGRES_PLAINTEXT_REJECTED="$postgres_plaintext_rejected" \
  E_POSTGRES_PLAINTEXT_STDERR_PRESENT="$postgres_plaintext_stderr_present" \
  E_POSTGRES_PLAINTEXT_STDERR_SHA256="$postgres_plaintext_stderr_sha256" \
  E_POSTGRES_SCRAM_PASSWORD_VERIFIED="$postgres_scram_password_verified" \
  E_POSTGRES_TLS_CONNECTIONS="$postgres_tls_connections" \
  E_POSTGRES_TLS_VERSION="$postgres_tls_version" E_POSTGRES_TLS_CIPHER="$postgres_tls_cipher" \
  E_POSTGRES_TLS_BITS="$postgres_tls_bits" E_POSTGRES_TLS_CA_SHA256="$postgres_tls_ca_sha256" \
  E_POSTGRES_TLS_LEAF_SHA256="$postgres_tls_leaf_sha256" E_POSTGRES_TLS_LEAF_SAN="$postgres_tls_leaf_san" \
  E_POSTGRES_TLS_LEAF_NOT_AFTER="$postgres_tls_leaf_not_after" \
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
    "schema": "asteria-drive-staging-monitor/v2",
    "status": os.environ["E_STATUS"],
    "last_phase": os.environ["E_PHASE"],
    "started_at": os.environ["E_STARTED_AT"],
    "completed_at": os.environ["E_COMPLETED_AT"],
    "github_run_id": os.environ["E_RUN_ID"],
    "github_run_attempt": os.environ["E_RUN_ATTEMPT"],
    "workflow_sha": os.environ["E_WORKFLOW_SHA"],
    "monitor_script_sha256": os.environ["E_MONITOR_SCRIPT_SHA256"],
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
    "postgres_tls_verified": boolean("E_POSTGRES_TLS_VERIFIED"),
    "postgres_tls_dsn_verified": boolean("E_POSTGRES_TLS_DSN_VERIFIED"),
    "postgres_hba_verified": boolean("E_POSTGRES_HBA_VERIFIED"),
    "postgres_plaintext_rejected": boolean("E_POSTGRES_PLAINTEXT_REJECTED"),
    "postgres_plaintext_stderr_present": boolean("E_POSTGRES_PLAINTEXT_STDERR_PRESENT"),
    "postgres_plaintext_stderr_sha256": os.environ["E_POSTGRES_PLAINTEXT_STDERR_SHA256"],
    "postgres_scram_password_verified": boolean("E_POSTGRES_SCRAM_PASSWORD_VERIFIED"),
    "postgres_tls_connections": integer("E_POSTGRES_TLS_CONNECTIONS"),
    "postgres_tls_version": os.environ["E_POSTGRES_TLS_VERSION"],
    "postgres_tls_cipher": os.environ["E_POSTGRES_TLS_CIPHER"],
    "postgres_tls_bits": integer("E_POSTGRES_TLS_BITS"),
    "postgres_tls_ca_sha256": os.environ["E_POSTGRES_TLS_CA_SHA256"],
    "postgres_tls_leaf_sha256": os.environ["E_POSTGRES_TLS_LEAF_SHA256"],
    "postgres_tls_leaf_san": os.environ["E_POSTGRES_TLS_LEAF_SAN"],
    "postgres_tls_leaf_not_after": os.environ["E_POSTGRES_TLS_LEAF_NOT_AFTER"],
    "capacity_max_disk_used_percent": integer("E_MAX_DISK_USED"),
    "capacity_min_disk_available_bytes": integer("E_MIN_DISK_AVAILABLE_KIB") * 1024,
}
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

finish() {
  local code="$?" evidence_code=0 cleanup_code=0
  trap - EXIT
  write_evidence || evidence_code="$?"
  cleanup_tls_temp || cleanup_code="$?"
  if [[ "$evidence_code" -ne 0 ]]; then
    printf 'staging monitor could not write evidence\n' >&2
    code=1
  fi
  if [[ "$cleanup_code" -ne 0 ]]; then
    printf 'staging monitor could not remove temporary TLS files\n' >&2
    code=1
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
verify_container asteria-drive-staging-api-1 api "$app_image" none
api_container_verified="true"
verify_container asteria-drive-staging-postgres-1 postgres "$postgres_image" healthy
postgres_container_verified="true"
verify_container asteria-drive-staging-seaweedfs-1 seaweedfs "$seaweedfs_image" healthy
seaweedfs_container_verified="true"

phase="http-readiness"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/readyz >/dev/null
readiness_succeeded="true"

phase="postgres-tls"
verify_postgres_tls

phase="loopback-bindings"
verify_loopback_bindings
loopback_bindings_verified="true"

phase="http-probes"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/healthz >/dev/null
health_succeeded="true"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:19090/metrics |
  awk '/^asteria_http_requests_total([ {]|$)/ { found = 1 } END { exit(found ? 0 : 1) }'
metrics_scrape_succeeded="true"

phase="complete"
status="success"
