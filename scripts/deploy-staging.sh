#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [[ "$#" -ne 5 ]]; then
  printf 'usage: deploy-staging.sh COMPOSE_SOURCE EVIDENCE_PATH RUN_ID RUN_ATTEMPT WORKFLOW_SHA\n' >&2
  exit 2
fi

compose_source="$1"
evidence_path="$2"
run_id="$3"
run_attempt="$4"
workflow_sha="$5"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
active_compose_file="$deploy_root/compose.yaml"
compose_file="$deploy_root/compose.${run_id}.${run_attempt}.candidate.yaml"
project="asteria-drive-staging"
release_tag="v0.1.1"
source_commit="e8d26ded6e9138bbfdeac60f2487c5f835ab61a5"
expected_image="ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"
postgres_image="postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa"
helper_image="chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
expected_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"
tenant_id="11111111-1111-4111-8111-111111111111"
max_disk_used_percent=85
min_disk_available_kib=$((5 * 1024 * 1024))

[[ "$run_id" =~ ^[0-9]{1,20}$ ]] || { printf 'run id is invalid\n' >&2; exit 1; }
[[ "$run_attempt" =~ ^[0-9]{1,6}$ ]] || { printf 'run attempt is invalid\n' >&2; exit 1; }
[[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || { printf 'workflow sha is invalid\n' >&2; exit 1; }
[[ "$deploy_root" =~ ^/opt/asteria-drive/[A-Za-z0-9._-]+$ ]] || { printf 'deploy root is invalid\n' >&2; exit 1; }

status="failed"
phase="initialize"
migration="false"
health="false"
ready="false"
authenticated_smoke="false"
upload_download_smoke="false"
metrics_scrape="false"
storage_verified="false"
binary_version_verified="false"
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
postgres_tls_leaf_not_before="none"
postgres_tls_leaf_not_after="none"
previous_compose_available="false"
runtime_changed="false"
rollback_attempted="false"
rollback_succeeded="false"
candidate_cleanup_attempted="false"
candidate_cleanup_succeeded="false"
failed_phase="none"
previous_image="none"
deployed_image_ref="none"
deployed_image_id="none"
compose_sha256="none"
verifier_checked=0
migration_schema_before=0
migration_schema_after=0
started_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
load_before="0"
load_after="0"
memory_used_before="0"
memory_used_after="0"
disk_used_before="0"
disk_used_after="0"
disk_available_kib_before="0"
disk_available_kib_after="0"
root_filesystem_before="unknown"
root_filesystem_after="unknown"
docker_filesystem_before="unknown"
docker_filesystem_after="unknown"
docker_disk_used_before="0"
docker_disk_used_after="0"
docker_disk_available_kib_before="0"
docker_disk_available_kib_after="0"
postgres_data_filesystem="unknown"
postgres_data_disk_used="0"
postgres_data_disk_available_kib="0"
seaweedfs_data_filesystem="unknown"
seaweedfs_data_disk_used="0"
seaweedfs_data_disk_available_kib="0"
smoke_dir=""
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
  local path="$1" output
  [[ -n "$path" && -d "$path" ]] || return 1
  output="$(df -Pk -- "$path")" || return 1
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

snapshot_host() {
  local prefix="$1" load memory
  load="$(awk '{print $1}' /proc/loadavg)"
  memory="$(awk '/MemTotal:/ {t=$2} /MemAvailable:/ {a=$2} END {printf "%.2f", (t-a)*100/t}' /proc/meminfo)"
  capture_host_filesystem "/" || return 1
  printf -v "load_${prefix}" '%s' "$load"
  printf -v "memory_used_${prefix}" '%s' "$memory"
  printf -v "root_filesystem_${prefix}" '%s' "$captured_filesystem"
  printf -v "disk_used_${prefix}" '%s' "$captured_used_percent"
  printf -v "disk_available_kib_${prefix}" '%s' "$captured_available_kib"
}

snapshot_docker() {
  local prefix="$1"
  capture_volume_filesystem "asteria-drive-staging-app-secrets" || return 1
  printf -v "docker_filesystem_${prefix}" '%s' "$captured_filesystem"
  printf -v "docker_disk_used_${prefix}" '%s' "$captured_used_percent"
  printf -v "docker_disk_available_kib_${prefix}" '%s' "$captured_available_kib"
}

verify_capacity() {
  local label="$1" used_percent="$2" available_kib="$3"
  if (( used_percent > max_disk_used_percent || available_kib < min_disk_available_kib )); then
    printf 'staging capacity guard failed: %s is %s%% used with %s KiB available\n' \
      "$label" "$used_percent" "$available_kib" >&2
    return 1
  fi
}

verify_data_volume_filesystems() {
  local expected_filesystem="$1" volume project_label
  for volume in asteria-drive-staging-postgres-data asteria-drive-staging-seaweedfs-data; do
    project_label="$(docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}' "$volume")" || return 1
    [[ "$project_label" == "$project" ]] || return 1
    capture_volume_filesystem "$volume" || return 1
    [[ "$captured_filesystem" == "$expected_filesystem" ]] || return 1
    verify_capacity "$volume" "$captured_used_percent" "$captured_available_kib" || return 1
    case "$volume" in
      asteria-drive-staging-postgres-data)
        postgres_data_filesystem="$captured_filesystem"
        postgres_data_disk_used="$captured_used_percent"
        postgres_data_disk_available_kib="$captured_available_kib"
        ;;
      asteria-drive-staging-seaweedfs-data)
        seaweedfs_data_filesystem="$captured_filesystem"
        seaweedfs_data_disk_used="$captured_used_percent"
        seaweedfs_data_disk_available_kib="$captured_available_kib"
        ;;
      *) return 1 ;;
    esac
  done
  data_volume_filesystems_verified="true"
}

write_evidence() {
  local completed_at tmp_path evidence_dir
  completed_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')" || return 1
  tmp_path="${evidence_path}.tmp"
  evidence_dir="$(dirname "$evidence_path")" || return 1
  install -d -m 0750 "$evidence_dir" || return 1
  rm -f -- "$tmp_path" || return 1
  if ! E_STATUS="$status" \
  E_PHASE="$phase" \
  E_RELEASE_TAG="$release_tag" \
  E_SOURCE_COMMIT="$source_commit" \
  E_IMAGE="$expected_image" \
  E_WORKFLOW_SHA="$workflow_sha" \
  E_RUN_ID="$run_id" \
  E_RUN_ATTEMPT="$run_attempt" \
  E_STARTED_AT="$started_at" \
  E_COMPLETED_AT="$completed_at" \
  E_COMPOSE_SHA256="$compose_sha256" \
  E_PREVIOUS_IMAGE="$previous_image" \
  E_DEPLOYED_IMAGE_REF="$deployed_image_ref" \
  E_DEPLOYED_IMAGE_ID="$deployed_image_id" \
  E_MIGRATION="$migration" \
  E_HEALTH="$health" \
  E_READY="$ready" \
  E_AUTH_SMOKE="$authenticated_smoke" \
  E_UPLOAD_DOWNLOAD="$upload_download_smoke" \
  E_METRICS="$metrics_scrape" \
  E_STORAGE_VERIFIED="$storage_verified" \
  E_BINARY_VERSION="$binary_version_verified" \
  E_LOOPBACK="$loopback_bindings_verified" \
  E_CAPACITY_GUARD="$capacity_guard_verified" \
  E_DATA_VOLUME_FILESYSTEMS="$data_volume_filesystems_verified" \
  E_POSTGRES_TLS_VERIFIED="$postgres_tls_verified" \
  E_POSTGRES_TLS_DSN_VERIFIED="$postgres_tls_dsn_verified" \
  E_POSTGRES_HBA_VERIFIED="$postgres_hba_verified" \
  E_POSTGRES_PLAINTEXT_REJECTED="$postgres_plaintext_rejected" \
  E_POSTGRES_PLAINTEXT_STDERR_PRESENT="$postgres_plaintext_stderr_present" \
  E_POSTGRES_PLAINTEXT_STDERR_SHA256="$postgres_plaintext_stderr_sha256" \
  E_POSTGRES_SCRAM_PASSWORD_VERIFIED="$postgres_scram_password_verified" \
  E_POSTGRES_TLS_CONNECTIONS="$postgres_tls_connections" \
  E_POSTGRES_TLS_VERSION="$postgres_tls_version" \
  E_POSTGRES_TLS_CIPHER="$postgres_tls_cipher" \
  E_POSTGRES_TLS_BITS="$postgres_tls_bits" \
  E_POSTGRES_TLS_CA_SHA256="$postgres_tls_ca_sha256" \
  E_POSTGRES_TLS_LEAF_SHA256="$postgres_tls_leaf_sha256" \
  E_POSTGRES_TLS_LEAF_SAN="$postgres_tls_leaf_san" \
  E_POSTGRES_TLS_LEAF_NOT_BEFORE="$postgres_tls_leaf_not_before" \
  E_POSTGRES_TLS_LEAF_NOT_AFTER="$postgres_tls_leaf_not_after" \
  E_PREVIOUS_COMPOSE_AVAILABLE="$previous_compose_available" \
  E_RUNTIME_CHANGED="$runtime_changed" \
  E_FAILED_PHASE="$failed_phase" \
  E_ROLLBACK_ATTEMPTED="$rollback_attempted" \
  E_ROLLBACK_SUCCEEDED="$rollback_succeeded" \
  E_CANDIDATE_CLEANUP_ATTEMPTED="$candidate_cleanup_attempted" \
  E_CANDIDATE_CLEANUP_SUCCEEDED="$candidate_cleanup_succeeded" \
  E_VERIFIER_CHECKED="$verifier_checked" \
  E_SCHEMA_BEFORE="$migration_schema_before" \
  E_SCHEMA_AFTER="$migration_schema_after" \
  E_LOAD_BEFORE="$load_before" \
  E_LOAD_AFTER="$load_after" \
  E_MEMORY_BEFORE="$memory_used_before" \
  E_MEMORY_AFTER="$memory_used_after" \
  E_DISK_BEFORE="$disk_used_before" \
  E_DISK_AFTER="$disk_used_after" \
  E_DISK_AVAILABLE_KIB_BEFORE="$disk_available_kib_before" \
  E_DISK_AVAILABLE_KIB_AFTER="$disk_available_kib_after" \
  E_ROOT_FILESYSTEM_BEFORE="$root_filesystem_before" \
  E_ROOT_FILESYSTEM_AFTER="$root_filesystem_after" \
  E_DOCKER_FILESYSTEM_BEFORE="$docker_filesystem_before" \
  E_DOCKER_FILESYSTEM_AFTER="$docker_filesystem_after" \
  E_DOCKER_DISK_BEFORE="$docker_disk_used_before" \
  E_DOCKER_DISK_AFTER="$docker_disk_used_after" \
  E_DOCKER_AVAILABLE_KIB_BEFORE="$docker_disk_available_kib_before" \
  E_DOCKER_AVAILABLE_KIB_AFTER="$docker_disk_available_kib_after" \
  E_POSTGRES_DATA_FILESYSTEM="$postgres_data_filesystem" \
  E_POSTGRES_DATA_DISK_USED="$postgres_data_disk_used" \
  E_POSTGRES_DATA_AVAILABLE_KIB="$postgres_data_disk_available_kib" \
  E_SEAWEEDFS_DATA_FILESYSTEM="$seaweedfs_data_filesystem" \
  E_SEAWEEDFS_DATA_DISK_USED="$seaweedfs_data_disk_used" \
  E_SEAWEEDFS_DATA_AVAILABLE_KIB="$seaweedfs_data_disk_available_kib" \
  E_MAX_DISK_USED_PERCENT="$max_disk_used_percent" \
  E_MIN_DISK_AVAILABLE_KIB="$min_disk_available_kib" \
  python3 - "$tmp_path" <<'PY'
import json
import os
import sys

def boolean(name):
    value = os.environ[name]
    if value not in {"true", "false"}:
        raise SystemExit(f"invalid boolean evidence value: {name}")
    return value == "true"

payload = {
    "schema": "asteria-drive-staging-deployment/v2",
    "status": os.environ["E_STATUS"],
    "last_phase": os.environ["E_PHASE"],
    "release_tag": os.environ["E_RELEASE_TAG"],
    "source_commit": os.environ["E_SOURCE_COMMIT"],
    "image": os.environ["E_IMAGE"],
    "workflow_sha": os.environ["E_WORKFLOW_SHA"],
    "github_run_id": os.environ["E_RUN_ID"],
    "github_run_attempt": os.environ["E_RUN_ATTEMPT"],
    "started_at": os.environ["E_STARTED_AT"],
    "completed_at": os.environ["E_COMPLETED_AT"],
    "compose_sha256": os.environ["E_COMPOSE_SHA256"],
    "previous_image": os.environ["E_PREVIOUS_IMAGE"],
    "deployed_image_ref": os.environ["E_DEPLOYED_IMAGE_REF"],
    "deployed_image_id": os.environ["E_DEPLOYED_IMAGE_ID"],
    "migration_succeeded": boolean("E_MIGRATION"),
    "health_succeeded": boolean("E_HEALTH"),
    "readiness_succeeded": boolean("E_READY"),
    "authenticated_smoke_succeeded": boolean("E_AUTH_SMOKE"),
    "upload_download_smoke_succeeded": boolean("E_UPLOAD_DOWNLOAD"),
    "metrics_scrape_succeeded": boolean("E_METRICS"),
    "storage_verifier_succeeded": boolean("E_STORAGE_VERIFIED"),
    "binary_version_verified": boolean("E_BINARY_VERSION"),
    "loopback_bindings_verified": boolean("E_LOOPBACK"),
    "capacity_guard_verified": boolean("E_CAPACITY_GUARD"),
    "data_volume_filesystems_verified": boolean("E_DATA_VOLUME_FILESYSTEMS"),
    "postgres_tls_verified": boolean("E_POSTGRES_TLS_VERIFIED"),
    "postgres_tls_dsn_verified": boolean("E_POSTGRES_TLS_DSN_VERIFIED"),
    "postgres_hba_verified": boolean("E_POSTGRES_HBA_VERIFIED"),
    "postgres_plaintext_rejected": boolean("E_POSTGRES_PLAINTEXT_REJECTED"),
    "postgres_plaintext_stderr_present": boolean("E_POSTGRES_PLAINTEXT_STDERR_PRESENT"),
    "postgres_plaintext_stderr_sha256": os.environ["E_POSTGRES_PLAINTEXT_STDERR_SHA256"],
    "postgres_scram_password_verified": boolean("E_POSTGRES_SCRAM_PASSWORD_VERIFIED"),
    "postgres_tls_connections": int(os.environ["E_POSTGRES_TLS_CONNECTIONS"]),
    "postgres_tls_version": os.environ["E_POSTGRES_TLS_VERSION"],
    "postgres_tls_cipher": os.environ["E_POSTGRES_TLS_CIPHER"],
    "postgres_tls_bits": int(os.environ["E_POSTGRES_TLS_BITS"]),
    "postgres_tls_ca_sha256": os.environ["E_POSTGRES_TLS_CA_SHA256"],
    "postgres_tls_leaf_sha256": os.environ["E_POSTGRES_TLS_LEAF_SHA256"],
    "postgres_tls_leaf_san": os.environ["E_POSTGRES_TLS_LEAF_SAN"],
    "postgres_tls_leaf_not_before": os.environ["E_POSTGRES_TLS_LEAF_NOT_BEFORE"],
    "postgres_tls_leaf_not_after": os.environ["E_POSTGRES_TLS_LEAF_NOT_AFTER"],
    "previous_compose_available": boolean("E_PREVIOUS_COMPOSE_AVAILABLE"),
    "runtime_changed": boolean("E_RUNTIME_CHANGED"),
    "failed_phase": os.environ["E_FAILED_PHASE"],
    "rollback_attempted": boolean("E_ROLLBACK_ATTEMPTED"),
    "rollback_succeeded": boolean("E_ROLLBACK_SUCCEEDED"),
    "candidate_cleanup_attempted": boolean("E_CANDIDATE_CLEANUP_ATTEMPTED"),
    "candidate_cleanup_succeeded": boolean("E_CANDIDATE_CLEANUP_SUCCEEDED"),
    "storage_verifier_checked": int(os.environ["E_VERIFIER_CHECKED"]),
    "migration_schema_before": int(os.environ["E_SCHEMA_BEFORE"]),
    "migration_schema_after": int(os.environ["E_SCHEMA_AFTER"]),
    "load1_before": float(os.environ["E_LOAD_BEFORE"]),
    "load1_after": float(os.environ["E_LOAD_AFTER"]),
    "memory_used_percent_before": float(os.environ["E_MEMORY_BEFORE"]),
    "memory_used_percent_after": float(os.environ["E_MEMORY_AFTER"]),
    "disk_used_percent_before": int(os.environ["E_DISK_BEFORE"]),
    "disk_used_percent_after": int(os.environ["E_DISK_AFTER"]),
    "disk_available_bytes_before": int(os.environ["E_DISK_AVAILABLE_KIB_BEFORE"]) * 1024,
    "disk_available_bytes_after": int(os.environ["E_DISK_AVAILABLE_KIB_AFTER"]) * 1024,
    "root_filesystem_before": os.environ["E_ROOT_FILESYSTEM_BEFORE"],
    "root_filesystem_after": os.environ["E_ROOT_FILESYSTEM_AFTER"],
    "docker_filesystem_before": os.environ["E_DOCKER_FILESYSTEM_BEFORE"],
    "docker_filesystem_after": os.environ["E_DOCKER_FILESYSTEM_AFTER"],
    "docker_data_root_on_root_filesystem": os.environ["E_ROOT_FILESYSTEM_AFTER"] == os.environ["E_DOCKER_FILESYSTEM_AFTER"],
    "docker_disk_used_percent_before": int(os.environ["E_DOCKER_DISK_BEFORE"]),
    "docker_disk_used_percent_after": int(os.environ["E_DOCKER_DISK_AFTER"]),
    "docker_disk_available_bytes_before": int(os.environ["E_DOCKER_AVAILABLE_KIB_BEFORE"]) * 1024,
    "docker_disk_available_bytes_after": int(os.environ["E_DOCKER_AVAILABLE_KIB_AFTER"]) * 1024,
    "postgres_data_filesystem": os.environ["E_POSTGRES_DATA_FILESYSTEM"],
    "postgres_data_disk_used_percent": int(os.environ["E_POSTGRES_DATA_DISK_USED"]),
    "postgres_data_disk_available_bytes": int(os.environ["E_POSTGRES_DATA_AVAILABLE_KIB"]) * 1024,
    "seaweedfs_data_filesystem": os.environ["E_SEAWEEDFS_DATA_FILESYSTEM"],
    "seaweedfs_data_disk_used_percent": int(os.environ["E_SEAWEEDFS_DATA_DISK_USED"]),
    "seaweedfs_data_disk_available_bytes": int(os.environ["E_SEAWEEDFS_DATA_AVAILABLE_KIB"]) * 1024,
    "capacity_max_disk_used_percent": int(os.environ["E_MAX_DISK_USED_PERCENT"]),
    "capacity_min_disk_available_bytes": int(os.environ["E_MIN_DISK_AVAILABLE_KIB"]) * 1024,
    "exposure": "loopback-bindings-verified",
    "secret_source": "server-managed-docker-volumes",
    "claim_boundary": "staging-not-production",
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(payload, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
  then
    rm -f -- "$tmp_path"
    return 1
  fi
  if ! chmod 0600 "$tmp_path"; then
    rm -f -- "$tmp_path"
    return 1
  fi
  if ! mv -f -- "$tmp_path" "$evidence_path"; then
    rm -f -- "$tmp_path"
    return 1
  fi
}

cleanup_smoke() {
  [[ -n "$smoke_dir" ]] || return 0
  local parent
  parent="$(dirname "$evidence_path")"
  [[ "$smoke_dir" =~ ^${parent//./\\.}/smoke\.[A-Za-z0-9]{8}$ ]] || return 1
  rm -rf -- "$smoke_dir"
  smoke_dir=""
}

verify_image_identity() {
  local container_id="$1" expected_ref="$2" actual_ref actual_id expected_id
  actual_ref="$(docker inspect --format '{{.Config.Image}}' "$container_id")" || return 1
  actual_id="$(docker inspect --format '{{.Image}}' "$container_id")" || return 1
  expected_id="$(docker image inspect --format '{{.Id}}' "$expected_ref")" || return 1
  [[ "$actual_ref" == "$expected_ref" && "$actual_id" == "$expected_id" ]]
}

rollback_runtime() {
  local restored_id
  [[ "$previous_compose_available" == "true" ]]
  [[ -f "$active_compose_file" && ! -L "$active_compose_file" ]]
  docker compose -p "$project" -f "$active_compose_file" --profile tools config --quiet
  docker compose -p "$project" -f "$active_compose_file" up -d --pull never --wait --wait-timeout 180 postgres seaweedfs
  docker compose -p "$project" -f "$active_compose_file" up -d --pull never --no-deps api
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error --max-time 3 http://127.0.0.1:18080/readyz >/dev/null; then
      restored_id="$(docker compose -p "$project" -f "$active_compose_file" ps -q api)" || return 1
      [[ -n "$restored_id" ]] || return 1
      verify_image_identity "$restored_id" "$previous_image" || return 1
      return 0
    fi
    sleep 2
  done
  return 1
}

cleanup_candidate_runtime() {
  [[ "$runtime_changed" == "true" ]]
  [[ -f "$compose_file" && ! -L "$compose_file" ]]
  docker compose -p "$project" -f "$compose_file" down --remove-orphans --timeout 30
}

verify_postgres_tls() {
  local ca_file="$smoke_dir/postgres-ca.crt"
  local leaf_file="$smoke_dir/postgres-server.crt"
  local settings hba_rules tls_row tls_all plaintext_stderr plaintext_exit
  local not_before_raw not_after_raw

  docker compose -p "$project" -f "$compose_file" exec -T postgres \
    cat /var/run/secrets/asteria/postgres-ca.crt >"$ca_file"
  docker compose -p "$project" -f "$compose_file" exec -T postgres \
    cat /var/run/secrets/asteria/postgres-server.crt >"$leaf_file"
  [[ -s "$ca_file" && -s "$leaf_file" ]]
  [[ "$(stat -c '%s' "$ca_file")" -le 16384 && "$(stat -c '%s' "$leaf_file")" -le 16384 ]]
  openssl verify -CAfile "$ca_file" -verify_hostname postgres "$leaf_file" >/dev/null
  openssl x509 -in "$leaf_file" -checkend $((30 * 24 * 60 * 60)) -noout
  postgres_tls_ca_sha256="$(openssl x509 -in "$ca_file" -outform DER | sha256sum | awk '{print $1}')"
  postgres_tls_leaf_sha256="$(openssl x509 -in "$leaf_file" -outform DER | sha256sum | awk '{print $1}')"
  [[ "$postgres_tls_ca_sha256" =~ ^[0-9a-f]{64}$ && "$postgres_tls_leaf_sha256" =~ ^[0-9a-f]{64}$ ]]
  postgres_tls_leaf_san="$(openssl x509 -in "$leaf_file" -noout -ext subjectAltName |
    awk 'NR > 1 { gsub(/[[:space:]]/, ""); printf "%s", $0 }')"
  [[ "$postgres_tls_leaf_san" == "DNS:postgres" ]]
  openssl x509 -in "$leaf_file" -noout -ext extendedKeyUsage |
    grep -Fq 'TLS Web Server Authentication'
  not_before_raw="$(openssl x509 -in "$leaf_file" -noout -startdate | cut -d= -f2-)"
  not_after_raw="$(openssl x509 -in "$leaf_file" -noout -enddate | cut -d= -f2-)"
  postgres_tls_leaf_not_before="$(date -u -d "$not_before_raw" +'%Y-%m-%dT%H:%M:%SZ')"
  postgres_tls_leaf_not_after="$(date -u -d "$not_after_raw" +'%Y-%m-%dT%H:%M:%SZ')"

  settings="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
    psql -U asteria -d asteria -AtF $'\t' -c \
      "SELECT current_setting('ssl'), current_setting('ssl_min_protocol_version'), current_setting('hba_file')")"
  [[ "$settings" == $'on\tTLSv1.2\t/var/run/secrets/asteria/pg_hba.conf' ]]
  hba_rules="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
    psql -U asteria -d asteria -Atqc \
      "SELECT COALESCE(string_agg(format('%s|%s|%s|%s|%s|%s|%s', type, array_to_string(database, ','), array_to_string(user_name, ','), COALESCE(address, ''), COALESCE(netmask, ''), auth_method, COALESCE(error, '')), E'\\n' ORDER BY line_number), '') FROM pg_hba_file_rules")"
  [[ "$hba_rules" == $'local|all|all|||trust|\nhostnossl|all|all|0.0.0.0|0.0.0.0|reject|\nhostnossl|all|all|::|::|reject|\nhostssl|all|all|0.0.0.0|0.0.0.0|scram-sha-256|\nhostssl|all|all|::|::|scram-sha-256|' ]]
  postgres_hba_verified="true"
  tls_row="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
    psql -U asteria -d asteria -AtF $'\t' -c \
      "SELECT count(*)::text, COALESCE(bool_and(s.ssl), false)::text, COALESCE(min(s.version), ''), COALESCE(min(s.cipher), ''), COALESCE(min(s.bits), 0)::text FROM pg_stat_activity a JOIN pg_stat_ssl s USING (pid) WHERE a.application_name = 'asteria-drive-staging-api'")"
  IFS=$'\t' read -r postgres_tls_connections tls_all postgres_tls_version postgres_tls_cipher postgres_tls_bits <<<"$tls_row"
  [[ "$postgres_tls_connections" =~ ^[0-9]+$ && "$postgres_tls_connections" -ge 1 ]]
  [[ "$tls_all" == "t" && "$postgres_tls_version" =~ ^TLSv1\.[23]$ ]]
  [[ "$postgres_tls_cipher" =~ ^[A-Za-z0-9_-]+$ ]]
  [[ "$postgres_tls_bits" =~ ^[0-9]+$ && "$postgres_tls_bits" -ge 128 ]]
  [[ "$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
    psql -U asteria -d asteria -Atqc \
      "SELECT COALESCE((SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname = 'asteria'), false)")" == "t" ]]
  postgres_scram_password_verified="true"

  plaintext_stderr="$smoke_dir/postgres-plaintext.stderr"
  set +e
  docker compose -p "$project" -f "$compose_file" exec -T --user root postgres sh -ec '
    export PGPASSWORD="$(cat /var/run/secrets/asteria/postgres-password)"
    exec psql "host=postgres port=5432 dbname=asteria user=asteria sslmode=disable connect_timeout=5" -Atqc "SELECT 1"
  ' >/dev/null 2>"$plaintext_stderr"
  plaintext_exit="$?"
  set -e
  [[ "$plaintext_exit" -ne 0 && -s "$plaintext_stderr" ]]
  postgres_plaintext_stderr_present="true"
  postgres_plaintext_stderr_sha256="$(sha256sum "$plaintext_stderr" | awk '{print $1}')"
  [[ "$postgres_plaintext_stderr_sha256" =~ ^[0-9a-f]{64}$ ]]
  rm -f -- "$plaintext_stderr"
  postgres_plaintext_rejected="true"
  postgres_tls_verified="true"
}

finish() {
  local code="$?" evidence_code=0
  trap - EXIT
  set +e
  if [[ "$code" -ne 0 ]]; then
    failed_phase="$phase"
  fi
  if ! snapshot_host after; then
    printf 'could not capture final staging host resource snapshot\n' >&2
    [[ "$code" -ne 0 ]] || phase="snapshot-after"
    code=1
  fi
  if docker image inspect "$helper_image" >/dev/null 2>&1; then
    if ! snapshot_docker after; then
      printf 'could not capture final Docker data resource snapshot\n' >&2
      [[ "$code" -ne 0 ]] || phase="snapshot-docker-after"
      code=1
    fi
  elif [[ "$code" -eq 0 ]]; then
    printf 'capacity probe image is unavailable for final snapshot\n' >&2
    code=1
    phase="snapshot-docker-after"
  fi
  if ! cleanup_smoke; then
    printf 'could not remove staging smoke files\n' >&2
    code=1
    phase="cleanup-smoke"
  fi
  if [[ "$code" -eq 0 ]]; then
    status="success"
    phase="complete"
    if ! write_evidence; then
      printf 'could not write deployment success evidence\n' >&2
      code=1
      failed_phase="write-evidence"
      phase="write-evidence"
    fi
  fi
  if [[ "$code" -eq 0 ]]; then
    phase="activate-compose"
    if mv -f -- "$compose_file" "$active_compose_file"; then
      compose_file="$active_compose_file"
    else
      printf 'could not activate the reviewed staging Compose file\n' >&2
      code=1
      failed_phase="activate-compose"
    fi
  fi
  if [[ "$code" -ne 0 ]]; then
    status="failed"
    [[ "$failed_phase" != "none" ]] || failed_phase="$phase"
    if [[ "$runtime_changed" == "true" && "$previous_compose_available" == "true" ]]; then
      rollback_attempted="true"
      phase="rollback"
      if rollback_runtime; then
        rollback_succeeded="true"
      else
        printf 'could not restore the previous staging runtime\n' >&2
      fi
    elif [[ "$runtime_changed" == "true" ]]; then
      candidate_cleanup_attempted="true"
      phase="candidate-cleanup"
      if cleanup_candidate_runtime; then
        candidate_cleanup_succeeded="true"
      else
        printf 'could not remove the first staging candidate runtime\n' >&2
      fi
    fi
    if [[ -f "$compose_file" && "$compose_file" != "$active_compose_file" ]]; then
      rm -f -- "$compose_file" || code=1
    fi
    phase="$failed_phase"
    write_evidence
    evidence_code="$?"
    if [[ "$evidence_code" -ne 0 ]]; then
      printf 'could not write deployment failure evidence\n' >&2
      code=1
    fi
  fi
  exit "$code"
}
trap finish EXIT

phase="validate-prerequisites"
for command in awk cmp curl df docker openssl python3 sha256sum; do
  command -v "$command" >/dev/null || { printf '%s is required\n' "$command" >&2; exit 1; }
done
docker compose version >/dev/null
for volume in \
  asteria-drive-staging-app-secrets \
  asteria-drive-staging-postgres-secrets \
  asteria-drive-staging-seaweedfs-secrets; do
  docker volume inspect "$volume" >/dev/null
done

snapshot_host before
phase="capacity-preflight"
verify_capacity "root filesystem" "$disk_used_before" "$disk_available_kib_before"

phase="validate-compose"
install -d -m 0750 "$deploy_root"
if [[ -e "$active_compose_file" ]]; then
  [[ -f "$active_compose_file" && ! -L "$active_compose_file" ]] || {
    printf 'active staging Compose path is unsafe\n' >&2
    exit 1
  }
  docker compose -p "$project" -f "$active_compose_file" --profile tools config --quiet
  previous_image="$(docker compose -p "$project" -f "$active_compose_file" \
    config --format json | python3 -c 'import json, sys; print(json.load(sys.stdin)["services"]["api"]["image"])')"
  [[ "$previous_image" =~ ^ghcr\.io/baicie/asteria-drive@sha256:[0-9a-f]{64}$ ]] || {
    printf 'active staging Compose has an invalid API image reference\n' >&2
    exit 1
  }
  previous_compose_available="true"
fi
install -m 0644 "$compose_source" "$compose_file"
compose_sha256="$(sha256sum "$compose_file" | awk '{print $1}')"
[[ "$compose_sha256" == "$expected_compose_sha256" ]] || { printf 'staging Compose digest is not approved\n' >&2; exit 1; }
grep -Fq "$expected_image" "$compose_file"
grep -Fq '127.0.0.1:18080:8080' "$compose_file"
grep -Fq '127.0.0.1:18333:8333' "$compose_file"
grep -Fq '127.0.0.1:19090:9090' "$compose_file"
if grep -Eq 'image:.*:latest|sha256:0{64}' "$compose_file"; then
  printf 'staging Compose contains a forbidden mutable or placeholder value\n' >&2
  exit 1
fi
docker compose -p "$project" -f "$compose_file" --profile tools config --quiet
docker compose -p "$project" -f "$compose_file" --profile tools config --format json |
  python3 -c '
import json
import sys

document = json.load(sys.stdin)
services = document["services"]
api = services["api"]["environment"]
migrate = services["migrate"]["environment"]
if api.get("ASTERIA_ENV") != "development" or migrate.get("ASTERIA_ENV") != "production":
    raise SystemExit("staging API/migration environment boundary is invalid")
expected_path = "/var/run/secrets/asteria/database-url-tls"
if api.get("ASTERIA_DATABASE_URL_FILE") != expected_path or migrate.get("ASTERIA_DATABASE_URL_FILE") != expected_path:
    raise SystemExit("staging TLS database URL path is invalid")
postgres = services["postgres"]
command = postgres.get("command", [])
for required in ("ssl=on", "ssl_min_protocol_version=TLSv1.2", "hba_file=/var/run/secrets/asteria/pg_hba.conf"):
    if required not in command:
        raise SystemExit("staging PostgreSQL TLS command is incomplete")
'

phase="prepare-capacity-probe"
docker pull --quiet "$helper_image" >/dev/null
snapshot_docker before
verify_capacity "Docker data filesystem" "$docker_disk_used_before" "$docker_disk_available_kib_before"

phase="pull-images"
docker compose -p "$project" -f "$compose_file" --profile tools pull --quiet
version_output="$(docker run --rm "$expected_image" --version)"
grep -Fq "$release_tag" <<<"$version_output"
grep -Fq "$source_commit" <<<"$version_output"
binary_version_verified="true"

phase="validate-postgres-tls-secrets"
smoke_dir="$(mktemp -d "$(dirname "$evidence_path")/smoke.XXXXXXXX")"
# Probe each volume as its file owner; only in-memory identities cross the ownership boundary.
app_secret_summary="$(docker run --rm --pull never --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 32 \
  --memory 64m --cpus 0.25 --log-driver none \
  --volume asteria-drive-staging-app-secrets:/app-secrets:ro \
  --entrypoint /bin/sh "$postgres_image" -ec '
    for specification in \
      "/app-secrets/database-url-tls 65532:65532:400" \
      "/app-secrets/postgres-ca.crt 65532:65532:400"; do
      path="${specification% *}"
      metadata="${specification##* }"
      [ -s "$path" ] && [ ! -L "$path" ] && [ "$(stat -c "%u:%g:%a" "$path")" = "$metadata" ]
    done
    dsn="$(cat /app-secrets/database-url-tls)"
    password="${dsn#postgres://asteria:}"
    password="${password%%@*}"
    printf "%s" "$password" | grep -Eq "^[0-9a-f]{48}$"
    expected="postgres://asteria:${password}@postgres:5432/asteria?sslmode=verify-full&sslrootcert=/var/run/secrets/asteria/postgres-ca.crt&application_name=asteria-drive-staging-api"
    [ "$dsn" = "$expected" ]
    password_sha256="$(printf "%s" "$password" | sha256sum | awk "{print \$1}")"
    ca_sha256="$(sha256sum /app-secrets/postgres-ca.crt | awk "{print \$1}")"
    printf "%s %s\n" "$password_sha256" "$ca_sha256"
  ')"
IFS=' ' read -r app_password_sha256 app_ca_sha256 app_summary_extra <<<"$app_secret_summary"
[[ "$app_password_sha256" =~ ^[0-9a-f]{64}$ && "$app_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$app_summary_extra" ]]

postgres_secret_summary="$(docker run --rm --pull never --network none --read-only --user 0:70 \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 32 \
  --memory 64m --cpus 0.25 --log-driver none \
  --volume asteria-drive-staging-postgres-secrets:/postgres-secrets:ro \
  --entrypoint /bin/sh "$postgres_image" -ec '
    for specification in \
      "/postgres-secrets/postgres-password 0:0:400" \
      "/postgres-secrets/postgres-ca.crt 0:70:640" \
      "/postgres-secrets/postgres-server.crt 0:70:640" \
      "/postgres-secrets/postgres-server.key 0:70:640" \
      "/postgres-secrets/pg_hba.conf 0:70:640"; do
      path="${specification% *}"
      metadata="${specification##* }"
      [ -s "$path" ] && [ ! -L "$path" ] && [ "$(stat -c "%u:%g:%a" "$path")" = "$metadata" ]
    done
    [ "$(grep -Ec "^(local|hostnossl|hostssl) " /postgres-secrets/pg_hba.conf)" -eq 5 ]
    grep -Fxq "hostnossl all all 0.0.0.0/0 reject" /postgres-secrets/pg_hba.conf
    grep -Fxq "hostnossl all all ::/0 reject" /postgres-secrets/pg_hba.conf
    grep -Fxq "hostssl all all 0.0.0.0/0 scram-sha-256" /postgres-secrets/pg_hba.conf
    grep -Fxq "hostssl all all ::/0 scram-sha-256" /postgres-secrets/pg_hba.conf
    password="$(cat /postgres-secrets/postgres-password)"
    printf "%s" "$password" | grep -Eq "^[0-9a-f]{48}$"
    password_sha256="$(printf "%s" "$password" | sha256sum | awk "{print \$1}")"
    ca_sha256="$(sha256sum /postgres-secrets/postgres-ca.crt | awk "{print \$1}")"
    printf "%s %s\n" "$password_sha256" "$ca_sha256"
  ')"
IFS=' ' read -r postgres_password_sha256 postgres_ca_sha256 postgres_summary_extra <<<"$postgres_secret_summary"
[[ "$postgres_password_sha256" =~ ^[0-9a-f]{64}$ && "$postgres_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$postgres_summary_extra" ]]
[[ "$app_password_sha256" == "$postgres_password_sha256" && "$app_ca_sha256" == "$postgres_ca_sha256" ]]
unset app_secret_summary app_password_sha256 app_ca_sha256 app_summary_extra
unset postgres_secret_summary postgres_password_sha256 postgres_ca_sha256 postgres_summary_extra
postgres_tls_dsn_verified="true"
docker run --rm --pull never --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
  --memory 32m --cpus 0.25 --log-driver none \
  --volume asteria-drive-staging-app-secrets:/app-secrets:ro \
  --entrypoint /bin/cat "$postgres_image" /app-secrets/postgres-ca.crt \
  >"$smoke_dir/preflight-postgres-ca.crt"
docker run --rm --pull never --network none --read-only --user 0:70 \
  --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
  --memory 32m --cpus 0.25 --log-driver none \
  --volume asteria-drive-staging-postgres-secrets:/postgres-secrets:ro \
  --entrypoint /bin/cat "$postgres_image" /postgres-secrets/postgres-server.crt \
  >"$smoke_dir/preflight-postgres-server.crt"
openssl verify -CAfile "$smoke_dir/preflight-postgres-ca.crt" -verify_hostname postgres \
  "$smoke_dir/preflight-postgres-server.crt" >/dev/null
openssl x509 -in "$smoke_dir/preflight-postgres-server.crt" -checkend $((30 * 24 * 60 * 60)) -noout >/dev/null
if [[ "$previous_compose_available" == "true" ]]; then
  previous_postgres_id="$(docker compose -p "$project" -f "$active_compose_file" ps -q postgres)"
  [[ -n "$previous_postgres_id" ]]
  [[ "$(docker exec --user postgres "$previous_postgres_id" psql -U asteria -d asteria -Atqc \
    "SELECT COALESCE((SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname = 'asteria'), false)")" == "t" ]]
fi

phase="capacity-prestart"
capture_volume_filesystem "asteria-drive-staging-app-secrets"
[[ "$captured_filesystem" == "$docker_filesystem_before" ]]
verify_capacity "Docker data filesystem" "$captured_used_percent" "$captured_available_kib"

phase="start-dependencies"
runtime_changed="true"
docker compose -p "$project" -f "$compose_file" up -d --wait --wait-timeout 180 postgres seaweedfs

phase="migrate"
migration_table_exists="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
  psql -U asteria -d asteria -Atqc "SELECT to_regclass('public.asteria_schema_migration') IS NOT NULL")"
if [[ "$migration_table_exists" == "t" ]]; then
  migration_schema_before="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
    psql -U asteria -d asteria -Atqc 'SELECT COALESCE(MAX(version),0) FROM asteria_schema_migration')"
fi
docker compose -p "$project" -f "$compose_file" run --rm migrate
migration_schema_after="$(docker compose -p "$project" -f "$compose_file" exec -T postgres \
  psql -U asteria -d asteria -Atqc 'SELECT COALESCE(MAX(version),0) FROM asteria_schema_migration')"
[[ "$migration_schema_after" -eq 3 ]] || { printf 'staging schema version is unexpected\n' >&2; exit 1; }
migration="true"

phase="start-api"
docker compose -p "$project" -f "$compose_file" up -d --no-deps api
for _ in $(seq 1 60); do
  if curl --fail --silent --show-error --max-time 3 http://127.0.0.1:18080/readyz >/dev/null; then
    ready="true"
    break
  fi
  sleep 2
done
[[ "$ready" == "true" ]] || { printf 'readiness did not become healthy\n' >&2; exit 1; }
curl --fail --silent --show-error --max-time 3 http://127.0.0.1:18080/healthz >/dev/null
health="true"

phase="verify-postgres-tls"
verify_postgres_tls

phase="verify-runtime-identity"
api_id="$(docker compose -p "$project" -f "$compose_file" ps -q api)"
deployed_image_ref="$(docker inspect --format '{{.Config.Image}}' "$api_id")"
deployed_image_id="$(docker inspect --format '{{.Image}}' "$api_id")"
verify_image_identity "$api_id" "$expected_image" || {
  printf 'running API image does not match the approved reference and local config ID\n' >&2
  exit 1
}
[[ "$(docker compose -p "$project" -f "$compose_file" port api 8080)" == "127.0.0.1:18080" ]]
[[ "$(docker compose -p "$project" -f "$compose_file" port api 9090)" == "127.0.0.1:19090" ]]
[[ "$(docker compose -p "$project" -f "$compose_file" port seaweedfs 8333)" == "127.0.0.1:18333" ]]
loopback_bindings_verified="true"

phase="authenticated-smoke"
token_json="$(docker run --rm --network none \
  -v asteria-drive-staging-app-secrets:/secrets:ro \
  --entrypoint /bin/sh "$helper_image" -ec 'cat /secrets/trusted-tokens.json')"
trusted_token="$(python3 -c 'import json,sys; data=json.load(sys.stdin); assert len(data)==1; print(next(iter(data)))' <<<"$token_json")"
[[ "${#trusted_token}" -ge 32 ]] || { printf 'trusted staging token could not be loaded\n' >&2; exit 1; }
unset token_json

authenticated_request() {
  local method="$1" url="$2" output="$3" data_file="${4:-}" idempotency_key="${5:-}"
  local arguments=(
    --config - --request "$method" --url "$url" --output "$output"
    --fail --silent --show-error --max-time 15
  )
  if [[ -n "$data_file" ]]; then
    arguments+=(--header 'Content-Type: application/json' --data-binary "@$data_file")
  fi
  if [[ -n "$idempotency_key" ]]; then
    arguments+=(--header "Idempotency-Key: $idempotency_key")
  fi
  printf 'header = "Authorization: Bearer %s"\nheader = "X-Tenant-ID: %s"\n' "$trusted_token" "$tenant_id" |
    curl "${arguments[@]}"
}

tenant_response="$smoke_dir/tenant.json"
authenticated_request GET http://127.0.0.1:18080/api/v1/tenant "$tenant_response"
root_id="$(python3 - "$tenant_response" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)["data"]
root = data["root_directory_id"]
if not isinstance(root, str) or len(root) != 36:
    raise SystemExit("invalid staging root directory id")
print(root)
PY
)"
authenticated_smoke="true"

phase="upload-download-smoke"
payload_file="$smoke_dir/payload.txt"
printf 'asteria staging deployment smoke run=%s attempt=%s\n' "$run_id" "$run_attempt" >"$payload_file"
payload_size="$(stat -c '%s' "$payload_file")"
create_request="$smoke_dir/create-request.json"
python3 - "$create_request" "$root_id" "$run_id" "$run_attempt" "$payload_size" <<'PY'
import json
import sys
path, root_id, run_id, attempt, size = sys.argv[1:]
with open(path, "w", encoding="utf-8") as handle:
    json.dump({
        "parent_id": root_id,
        "name": f"deployment-smoke-{run_id}-{attempt}.txt",
        "size": int(size),
        "mime_type": "text/plain",
    }, handle)
PY
create_response="$smoke_dir/create-response.json"
authenticated_request POST http://127.0.0.1:18080/api/v1/uploads "$create_response" \
  "$create_request" "staging-deploy-$run_id-$run_attempt"
upload_id="$(python3 - "$create_response" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)["data"]
if data.get("status") != "created" or data.get("part_size", 0) < 5 * 1024 * 1024:
    raise SystemExit("invalid staging upload session")
print(data["id"])
PY
)"

sign_request="$smoke_dir/sign-request.json"
printf '{"part_number":1}\n' >"$sign_request"
sign_response="$smoke_dir/sign-response.json"
authenticated_request POST "http://127.0.0.1:18080/api/v1/uploads/$upload_id/parts/sign" \
  "$sign_response" "$sign_request"
upload_curl_config="$smoke_dir/upload.curl"
python3 - "$sign_response" "$upload_curl_config" <<'PY'
import json
import sys
import urllib.parse

with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)["data"]
url = data.get("url", "")
parsed = urllib.parse.urlsplit(url)
if any(character in url for character in '\r\n"'):
    raise SystemExit("unsafe staging signed upload URL")
if data.get("method") != "PUT" or parsed.scheme != "http" or parsed.hostname != "seaweedfs" or parsed.port != 8333:
    raise SystemExit("invalid staging signed upload target")
headers = data.get("required_headers", {})
if not isinstance(headers, dict):
    raise SystemExit("invalid staging signed upload headers")
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write(f'url = "{url}"\nrequest = "PUT"\nfail\nsilent\nshow-error\nmax-time = 30\n')
    for name, value in headers.items():
        if not isinstance(name, str) or not isinstance(value, str) or any(c in name + value for c in '\r\n"'):
            raise SystemExit("unsafe staging signed upload header")
        handle.write(f'header = "{name}: {value}"\n')
PY
upload_headers="$smoke_dir/upload-headers.txt"
curl --config "$upload_curl_config" \
  --connect-to seaweedfs:8333:127.0.0.1:18333 \
  --data-binary "@$payload_file" --dump-header "$upload_headers" --output "$smoke_dir/upload-response.txt"
etag="$(python3 - "$upload_headers" <<'PY'
import email.parser
import sys

with open(sys.argv[1], "rb") as handle:
    blocks = [block for block in handle.read().split(b"\r\n\r\n") if block.strip()]
for block in reversed(blocks):
    header_lines = block.split(b"\r\n", 1)
    if len(header_lines) != 2:
        continue
    headers = email.parser.BytesParser().parsebytes(header_lines[1] + b"\r\n\r\n")
    etag = headers.get("ETag")
    if etag:
        print(etag)
        break
else:
    raise SystemExit("signed staging upload returned no ETag")
PY
)"

complete_request="$smoke_dir/complete-request.json"
python3 - "$complete_request" "$etag" "$payload_size" <<'PY'
import json
import sys
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump({"parts": [{"part_number": 1, "etag": sys.argv[2], "size": int(sys.argv[3])}]}, handle)
PY
complete_response="$smoke_dir/complete-response.json"
authenticated_request POST "http://127.0.0.1:18080/api/v1/uploads/$upload_id/complete" \
  "$complete_response" "$complete_request"
file_id="$(python3 - "$complete_response" "$payload_size" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)["data"]
if data["upload"].get("status") != "committed" or data["file"].get("size") != int(sys.argv[2]):
    raise SystemExit("staging upload did not commit")
print(data["file"]["id"])
PY
)"

download_response="$smoke_dir/download-response.json"
authenticated_request POST "http://127.0.0.1:18080/api/v1/files/$file_id/download-authorizations" \
  "$download_response"
download_curl_config="$smoke_dir/download.curl"
python3 - "$download_response" "$download_curl_config" <<'PY'
import json
import sys
import urllib.parse

with open(sys.argv[1], encoding="utf-8") as handle:
    data = json.load(handle)["data"]
url = data.get("url", "")
parsed = urllib.parse.urlsplit(url)
if any(character in url for character in '\r\n"'):
    raise SystemExit("unsafe staging signed download URL")
if data.get("method") != "GET" or parsed.scheme != "http" or parsed.hostname != "seaweedfs" or parsed.port != 8333:
    raise SystemExit("invalid staging signed download target")
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write(f'url = "{url}"\nrequest = "GET"\nfail\nsilent\nshow-error\nmax-time = 30\n')
PY
download_file="$smoke_dir/downloaded.txt"
curl --config "$download_curl_config" \
  --connect-to seaweedfs:8333:127.0.0.1:18333 --output "$download_file"
cmp --silent "$payload_file" "$download_file"
upload_download_smoke="true"

phase="metrics-scrape"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:19090/metrics >"$smoke_dir/metrics.txt"
grep -Eq '^asteria_http_requests_total|^# HELP asteria_http_requests_total' "$smoke_dir/metrics.txt"
metrics_scrape="true"
unset trusted_token

phase="storage-verifier"
verifier_path="$(dirname "$evidence_path")/storage-verifier.json"
verifier_tmp="${verifier_path}.tmp"
set +e
docker compose -p "$project" -f "$compose_file" run --rm verifier >"$verifier_tmp"
verifier_exit="$?"
set -e
python3 - "$verifier_tmp" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    report = json.load(handle)
required = {"schema_version", "checked", "healthy", "finding_counts", "findings", "findings_truncated", "verified"}
if not required.issubset(report):
    raise SystemExit("storage verifier report is incomplete")
if report["findings"] is not None and not isinstance(report["findings"], list):
    raise SystemExit("storage verifier report has invalid findings")
if not isinstance(report["finding_counts"], dict):
    raise SystemExit("storage verifier report has invalid finding fields")
PY
mv -f -- "$verifier_tmp" "$verifier_path"
verifier_checked="$(python3 - "$verifier_path" <<'PY'
import json
import sys
with open(sys.argv[1], encoding="utf-8") as handle:
    report = json.load(handle)
if report["schema_version"] != 3:
    raise SystemExit("storage verifier schema version is unexpected")
if report["checked"] < 1 or report["healthy"] != report["checked"]:
    raise SystemExit("storage verifier did not check a healthy persisted object")
if report["verified"] is not True or report["findings"] not in (None, []) or report["finding_counts"] or report["findings_truncated"] is not False:
    raise SystemExit("storage verifier reported findings")
print(report["checked"])
PY
)"
[[ "$verifier_exit" -eq 0 ]] || { printf 'storage verifier exited unsuccessfully\n' >&2; exit 1; }
storage_verified="true"

phase="capacity-postflight"
snapshot_host after
snapshot_docker after
verify_capacity "root filesystem" "$disk_used_after" "$disk_available_kib_after"
verify_capacity "Docker data filesystem" "$docker_disk_used_after" "$docker_disk_available_kib_after"
verify_data_volume_filesystems "$docker_filesystem_after"
capacity_guard_verified="true"
