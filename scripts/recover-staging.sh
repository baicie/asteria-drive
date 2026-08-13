#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

if [[ "$#" -ne 3 ]]; then
  printf 'usage: recover-staging.sh RUN_ID RUN_ATTEMPT WORKFLOW_SHA\n' >&2
  exit 2
fi

run_id="$1"
run_attempt="$2"
workflow_sha="$3"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
active_compose_file="$deploy_root/compose.yaml"
project="asteria-drive-staging"
source_api="asteria-drive-staging-api-1"
source_postgres="asteria-drive-staging-postgres-1"
source_seaweedfs="asteria-drive-staging-seaweedfs-1"
expected_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"
expected_app_image="sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"
expected_postgres_image="sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa"
expected_seaweedfs_image="sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
app_image="ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"
postgres_image="postgres:17.5-alpine@sha256:6567bca8d7bc8c82c5922425a0baee57be8402df92bae5eacad5f01ae9544daa"
helper_image="chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
resource_boundary="staging-recovery-ephemeral"
max_disk_used_percent=85
min_disk_available_kib=$((5 * 1024 * 1024))
max_archive_bytes=$((512 * 1024 * 1024))

[[ "$run_id" =~ ^[0-9]{1,20}$ ]] || { printf 'run id is invalid\n' >&2; exit 1; }
[[ "$run_attempt" =~ ^[0-9]{1,6}$ ]] || { printf 'run attempt is invalid\n' >&2; exit 1; }
[[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || { printf 'workflow sha is invalid\n' >&2; exit 1; }
[[ "$deploy_root" =~ ^/opt/asteria-drive/[A-Za-z0-9._-]+$ ]] || {
  printf 'deploy root is invalid\n' >&2
  exit 1
}

suffix="$run_id-$run_attempt"
work_dir="/tmp/asteria-staging-recovery.$run_id.$run_attempt"
target_container="asteria-staging-recovery-postgres-$suffix"
api_container="asteria-staging-recovery-api-$suffix"
verifier_container="asteria-staging-recovery-verifier-$suffix"
auth_container="asteria-staging-recovery-auth-$suffix"
capacity_container="asteria-staging-recovery-capacity-$suffix"
target_volume="asteria-staging-recovery-pgdata-$suffix"
recovery_network="asteria-staging-recovery-net-$suffix"
for resource in "$target_container" "$api_container" "$verifier_container" "$auth_container" "$capacity_container" "$target_volume" "$recovery_network"; do
  [[ "$resource" =~ ^asteria-staging-recovery-[a-z0-9-]+$ ]] || {
    printf 'derived recovery resource name is invalid\n' >&2
    exit 1
  }
done
[[ "$work_dir" =~ ^/tmp/asteria-staging-recovery\.[0-9]{1,20}\.[0-9]{1,6}$ ]] || exit 1

status="failed"
phase="initialize"
started_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
backup_catalog_verified="false"
source_stable_during_backup="false"
restore_succeeded="false"
schema_verified="false"
table_counts_verified="false"
storage_verifier_succeeded="false"
recovered_health_succeeded="false"
recovered_readiness_succeeded="false"
recovered_authenticated_read_succeeded="false"
recovered_metrics_scrape_succeeded="false"
cleanup_verified="false"
capacity_guard_verified="false"
object_versions_restored="false"
pitr_wal_replayed="false"
source_postgres_tls_verified="false"
source_postgres_tls_dsn_verified="false"
source_postgres_hba_verified="false"
source_postgres_tls_connections=0
source_postgres_tls_version="none"
source_postgres_tls_cipher="none"
source_postgres_tls_bits=0
archive_size_bytes=0
archive_sha256="0000000000000000000000000000000000000000000000000000000000000000"
source_schema_version=0
restored_schema_version=0
tables_checked=0
source_total_rows=0
restored_total_rows=0
row_counts_sha256="0000000000000000000000000000000000000000000000000000000000000000"
verifier_checked=0
verifier_healthy=0
verifier_finding_count=0
verifier_findings_truncated="false"
backup_elapsed_seconds=0
restore_elapsed_seconds=0
disk_used_percent_before=0
disk_available_kib_before=0
disk_used_percent_after=0
disk_available_kib_after=0
work_dir_created="false"
network_created="false"
volume_created="false"
target_created="false"
api_created="false"
verifier_created="false"
auth_created="false"
capacity_created="false"
seaweedfs_connected="false"
captured_available_kib=""
captured_used_percent=""
source_api_id=""
source_postgres_id=""
source_seaweedfs_id=""

staging_volumes=(
  asteria-drive-staging-postgres-data
  asteria-drive-staging-seaweedfs-data
  asteria-drive-staging-app-secrets
  asteria-drive-staging-postgres-secrets
  asteria-drive-staging-seaweedfs-secrets
)

tables=(
  asteria_schema_migration
  tenant
  file_node
  blob
  file_version
  upload_session
  upload_part
  principal
  tenant_member
  idempotency_record
  tenant_invitation
  tenant_group
  tenant_group_member
  node_acl
  audit_event
)

for command in awk chmod cmp curl date docker df head install mv openssl python3 rm seq sha256sum sleep stat timeout; do
  command -v "$command" >/dev/null || { printf '%s is required\n' "$command" >&2; exit 1; }
done

parse_capacity_output() {
  local output="$1" parsed available used extra
  parsed="$(printf '%s\n' "$output" | awk '
    NR == 1 { next }
    NR == 2 {
      gsub(/%/, "", $5)
      if (NF != 6 || $4 !~ /^[0-9]+$/ || $5 !~ /^[0-9]+$/) exit 1
      print $4 "\t" $5
      rows = 1
      next
    }
    NF > 0 { exit 1 }
    END { if (rows != 1) exit 1 }
  ')" || return 1
  IFS=$'\t' read -r available used extra <<<"$parsed"
  [[ "$available" =~ ^[0-9]+$ && "$used" =~ ^[0-9]+$ && -z "$extra" ]] || return 1
  captured_available_kib="$available"
  captured_used_percent="$used"
}

capture_path_capacity() {
  local path="$1" output
  output="$(df -Pk -- "$path")" || return 1
  parse_capacity_output "$output"
}

capture_host_capacity() {
  capture_path_capacity /
}

verify_capacity() {
  local used="$1" available="$2"
  (( used <= max_disk_used_percent && available >= min_disk_available_kib ))
}

remove_capacity_probe() {
  resource_labels_match container "$capacity_container" || return 1
  docker rm -f "$capacity_container" >/dev/null 2>&1 || return 1
  resource_absent container "$capacity_container" || return 1
  capacity_created="false"
}

verify_volume_capacity() {
  local volume="$1" driver options output
  [[ "$volume" =~ ^asteria-drive-staging-[a-z0-9-]+$ ||
     "$volume" == "$target_volume" ]] || return 1
  driver="$(docker volume inspect --format '{{.Driver}}' "$volume")" || return 1
  options="$(docker volume inspect --format '{{json .Options}}' "$volume")" || return 1
  [[ "$driver" == "local" && ( "$options" == "null" || "$options" == '{}' ) ]] || return 1
  if [[ "$volume" == "$target_volume" ]]; then
    resource_labels_match volume "$target_volume" || return 1
  fi
  capacity_created="true"
  output="$(timeout 30 docker run --pull never --name "$capacity_container" --network none \
    --label "com.baicie.asteria.boundary=$resource_boundary" \
    --label "com.baicie.asteria.run-id=$run_id" \
    --label "com.baicie.asteria.run-attempt=$run_attempt" \
    --user 65532:65532 --read-only --cap-drop ALL \
    --security-opt no-new-privileges:true --pids-limit 16 --memory 32m --cpus 0.25 \
    --log-driver none --mount "type=volume,source=$volume,target=/capacity,readonly" \
    --entrypoint /bin/sh "$helper_image" -c 'df -Pk /capacity')" || return 1
  remove_capacity_probe || return 1
  if [[ "$volume" == "$target_volume" ]]; then
    resource_labels_match volume "$target_volume" || return 1
  fi
  parse_capacity_output "$output" || return 1
  verify_capacity "$captured_used_percent" "$captured_available_kib"
}

verify_capacity_scope() {
  local volume root_available root_used
  capture_host_capacity || return 1
  root_available="$captured_available_kib"
  root_used="$captured_used_percent"
  verify_capacity "$root_used" "$root_available" || return 1
  for volume in "${staging_volumes[@]}"; do
    verify_volume_capacity "$volume" || return 1
  done
  captured_available_kib="$root_available"
  captured_used_percent="$root_used"
}

container_id() {
  local id
  id="$(docker inspect --format '{{.Id}}' "$1")" || return 1
  [[ "$id" =~ ^[0-9a-f]{64}$ ]] || return 1
  printf '%s' "$id"
}

verify_source_ids() {
  [[ "$(container_id "$source_api")" == "$source_api_id" ]] || return 1
  [[ "$(container_id "$source_postgres")" == "$source_postgres_id" ]] || return 1
  [[ "$(container_id "$source_seaweedfs")" == "$source_seaweedfs_id" ]] || return 1
}

verify_app_secret_metadata() {
  local app_summary postgres_summary app_password_sha256 app_ca_sha256 app_extra
  local postgres_password_sha256 postgres_ca_sha256 postgres_extra
  capacity_created="true"
  timeout 30 docker run --pull never --name "$capacity_container" --network none \
    --label "com.baicie.asteria.boundary=$resource_boundary" \
    --label "com.baicie.asteria.run-id=$run_id" \
    --label "com.baicie.asteria.run-attempt=$run_attempt" \
    --user 65532:65532 --read-only --cap-drop ALL \
    --security-opt no-new-privileges:true --pids-limit 16 --memory 32m --cpus 0.25 \
    --log-driver none \
    --mount type=volume,source=asteria-drive-staging-app-secrets,target=/secrets,readonly \
    --entrypoint /bin/sh "$helper_image" -ec '
      for name in database-url database-url-tls postgres-ca.crt cursor-hmac-key trusted-tokens.json s3-access-key-id s3-secret-access-key; do
        path="/secrets/$name"
        [ -f "$path" ] && [ ! -L "$path" ] && [ -s "$path" ] || exit 1
        [ "$(stat -c "%u:%g:%a" "$path")" = "65532:65532:400" ] || exit 1
      done
    ' >/dev/null || return 1
  remove_capacity_probe || return 1
  app_summary="$(docker run --rm --pull never --network none --read-only --user 65532:65532 \
    --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
    --memory 32m --cpus 0.25 --log-driver none \
    --mount type=volume,source=asteria-drive-staging-app-secrets,target=/app-secrets,readonly \
    --entrypoint /bin/sh "$helper_image" -ec '
      dsn="$(cat /app-secrets/database-url-tls)"
      password="${dsn#postgres://asteria:}"
      password="${password%%@*}"
      printf "%s" "$password" | grep -Eq "^[0-9a-f]{48}$"
      expected="postgres://asteria:${password}@postgres:5432/asteria?sslmode=verify-full&sslrootcert=/var/run/secrets/asteria/postgres-ca.crt&application_name=asteria-drive-staging-api"
      [ "$dsn" = "$expected" ]
      printf "%s %s\n" \
        "$(printf "%s" "$password" | sha256sum | awk "{print \$1}")" \
        "$(sha256sum /app-secrets/postgres-ca.crt | awk "{print \$1}")"
    ')" || return 1
  IFS=' ' read -r app_password_sha256 app_ca_sha256 app_extra <<<"$app_summary"
  [[ "$app_password_sha256" =~ ^[0-9a-f]{64}$ && "$app_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$app_extra" ]] || return 1

  postgres_summary="$(docker run --rm --pull never --network none --read-only --user 0:70 \
    --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 16 \
    --memory 32m --cpus 0.25 --log-driver none \
    --mount type=volume,source=asteria-drive-staging-postgres-secrets,target=/postgres-secrets,readonly \
    --entrypoint /bin/sh "$helper_image" -ec '
      password="$(cat /postgres-secrets/postgres-password)"
      printf "%s" "$password" | grep -Eq "^[0-9a-f]{48}$"
      printf "%s %s\n" \
        "$(printf "%s" "$password" | sha256sum | awk "{print \$1}")" \
        "$(sha256sum /postgres-secrets/postgres-ca.crt | awk "{print \$1}")"
    ')" || return 1
  IFS=' ' read -r postgres_password_sha256 postgres_ca_sha256 postgres_extra <<<"$postgres_summary"
  [[ "$postgres_password_sha256" =~ ^[0-9a-f]{64}$ && "$postgres_ca_sha256" =~ ^[0-9a-f]{64}$ && -z "$postgres_extra" ]] || return 1
  [[ "$app_password_sha256" == "$postgres_password_sha256" && "$app_ca_sha256" == "$postgres_ca_sha256" ]] || return 1
  unset app_summary app_password_sha256 app_ca_sha256 app_extra
  unset postgres_summary postgres_password_sha256 postgres_ca_sha256 postgres_extra
  source_postgres_tls_dsn_verified="true"
}

verify_image_identity() {
  local name="$1" expected_ref="$2" actual_image actual_ref expected_id
  actual_image="$(docker inspect --format '{{.Image}}' "$name")" || return 1
  actual_ref="$(docker inspect --format '{{.Config.Image}}' "$name")" || return 1
  expected_id="$(docker image inspect --format '{{.Id}}' "$expected_ref")" || return 1
  [[ "$actual_ref" == "$expected_ref" && "$actual_image" == "$expected_id" ]]
}

verify_container() {
  local name="$1" service="$2" image_ref="$3" expected_health="$4"
  local running project_label service_label health
  running="$(docker inspect --format '{{.State.Running}}' "$name")" || return 1
  project_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$name")" || return 1
  service_label="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$name")" || return 1
  [[ "$running" == "true" ]] || return 1
  verify_image_identity "$name" "$image_ref" || return 1
  [[ "$project_label" == "$project" && "$service_label" == "$service" ]] || return 1
  if [[ "$expected_health" != "none" ]]; then
    health="$(docker inspect --format '{{.State.Health.Status}}' "$name")" || return 1
    [[ "$health" == "$expected_health" ]] || return 1
  fi
}

resource_labels_match() {
  local kind="$1" name="$2" boundary run attempt
  case "$kind" in
    container)
      boundary="$(docker inspect --format '{{ index .Config.Labels "com.baicie.asteria.boundary" }}' "$name")" || return 1
      run="$(docker inspect --format '{{ index .Config.Labels "com.baicie.asteria.run-id" }}' "$name")" || return 1
      attempt="$(docker inspect --format '{{ index .Config.Labels "com.baicie.asteria.run-attempt" }}' "$name")" || return 1
      ;;
    network)
      boundary="$(docker network inspect --format '{{ index .Labels "com.baicie.asteria.boundary" }}' "$name")" || return 1
      run="$(docker network inspect --format '{{ index .Labels "com.baicie.asteria.run-id" }}' "$name")" || return 1
      attempt="$(docker network inspect --format '{{ index .Labels "com.baicie.asteria.run-attempt" }}' "$name")" || return 1
      ;;
    volume)
      boundary="$(docker volume inspect --format '{{ index .Labels "com.baicie.asteria.boundary" }}' "$name")" || return 1
      run="$(docker volume inspect --format '{{ index .Labels "com.baicie.asteria.run-id" }}' "$name")" || return 1
      attempt="$(docker volume inspect --format '{{ index .Labels "com.baicie.asteria.run-attempt" }}' "$name")" || return 1
      ;;
    *) return 1 ;;
  esac
  [[ "$boundary" == "$resource_boundary" && "$run" == "$run_id" && "$attempt" == "$run_attempt" ]]
}

resource_absent() {
  local kind="$1" name="$2"
  case "$kind" in
    container) ! docker container inspect "$name" >/dev/null 2>&1 ;;
    network) ! docker network inspect "$name" >/dev/null 2>&1 ;;
    volume) ! docker volume inspect "$name" >/dev/null 2>&1 ;;
    *) return 1 ;;
  esac
}

cleanup_resources() {
  local failed=0
  for container in "$capacity_container" "$auth_container" "$verifier_container" "$api_container" "$target_container"; do
    case "$container" in
      "$capacity_container") flag_name="capacity_created" ;;
      "$auth_container") flag_name="auth_created" ;;
      "$verifier_container") flag_name="verifier_created" ;;
      "$api_container") flag_name="api_created" ;;
      "$target_container") flag_name="target_created" ;;
    esac
    if [[ "${!flag_name}" == "true" ]]; then
      if resource_labels_match container "$container" &&
         docker rm -f "$container" >/dev/null 2>&1 &&
         resource_absent container "$container"; then
        printf -v "$flag_name" '%s' false
      else
        failed=1
      fi
    fi
  done
  if [[ "$seaweedfs_connected" == "true" ]]; then
    if resource_labels_match network "$recovery_network" &&
       docker network disconnect -f "$recovery_network" "$source_seaweedfs_id" >/dev/null 2>&1; then
      seaweedfs_connected="false"
    else
      failed=1
    fi
  fi
  if [[ "$network_created" == "true" ]]; then
    if resource_labels_match network "$recovery_network" &&
       docker network rm "$recovery_network" >/dev/null 2>&1 &&
       resource_absent network "$recovery_network"; then
      network_created="false"
    else
      failed=1
    fi
  fi
  if [[ "$volume_created" == "true" ]]; then
    if resource_labels_match volume "$target_volume" &&
       docker volume rm "$target_volume" >/dev/null 2>&1 &&
       resource_absent volume "$target_volume"; then
      volume_created="false"
    else
      failed=1
    fi
  fi
  if [[ "$work_dir_created" == "true" ]]; then
    if [[ "$work_dir" =~ ^/tmp/asteria-staging-recovery\.[0-9]{1,20}\.[0-9]{1,6}$ &&
          -d "$work_dir" && ! -L "$work_dir" && -O "$work_dir" ]] &&
       rm -rf -- "$work_dir" && [[ ! -e "$work_dir" ]]; then
      work_dir_created="false"
    else
      failed=1
    fi
  fi
  if [[ "$api_created" == "false" && "$target_created" == "false" &&
        "$verifier_created" == "false" && "$auth_created" == "false" &&
        "$capacity_created" == "false" &&
        "$seaweedfs_connected" == "false" && "$network_created" == "false" &&
        "$volume_created" == "false" && "$work_dir_created" == "false" && "$failed" -eq 0 ]] &&
     resource_absent container "$auth_container" &&
     resource_absent container "$capacity_container" &&
     resource_absent container "$verifier_container" &&
     resource_absent container "$api_container" &&
     resource_absent container "$target_container" &&
     resource_absent network "$recovery_network" &&
     resource_absent volume "$target_volume" &&
     [[ ! -e "$work_dir" ]]; then
    cleanup_verified="true"
    return 0
  fi
  cleanup_verified="false"
  return 1
}

capture_counts() {
  local container="$1" destination="$2" table count
  : >"$destination"
  for table in "${tables[@]}"; do
    count="$(docker exec --user postgres "$container" \
      psql --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 \
      --username asteria --dbname asteria --command "SELECT count(*) FROM $table" \
      2>"$work_dir/counts.stderr")" || return 1
    count="${count//$'\r'/}"
    count="${count//$'\n'/}"
    [[ "$count" =~ ^[0-9]+$ ]] || return 1
    printf '%s\t%s\n' "$table" "$count" >>"$destination"
  done
}

count_total() {
  awk -F '\t' '
    NF != 2 || $2 !~ /^[0-9]+$/ { exit 1 }
    { total += $2; rows += 1 }
    END { if (rows == 0) exit 1; printf "%.0f", total }
  ' "$1"
}

query_schema_version() {
  local container="$1" version
  version="$(docker exec --user postgres "$container" \
    psql --no-psqlrc --tuples-only --no-align --set ON_ERROR_STOP=1 \
    --username asteria --dbname asteria \
    --command 'SELECT COALESCE(MAX(version),0) FROM asteria_schema_migration' \
    2>"$work_dir/schema.stderr")" || return 1
  version="${version//$'\r'/}"
  version="${version//$'\n'/}"
  [[ "$version" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$version"
}

write_evidence() {
  local completed_at
  completed_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')" || return 1
  E_STATUS="$status" E_PHASE="$phase" E_STARTED_AT="$started_at" E_COMPLETED_AT="$completed_at" \
  E_RUN_ID="$run_id" E_RUN_ATTEMPT="$run_attempt" E_WORKFLOW_SHA="$workflow_sha" \
  E_COMPOSE_SHA="$expected_compose_sha256" E_APP_IMAGE="$expected_app_image" \
  E_POSTGRES_IMAGE="$expected_postgres_image" E_BACKUP_CATALOG="$backup_catalog_verified" \
  E_SOURCE_STABLE="$source_stable_during_backup" E_RESTORE="$restore_succeeded" \
  E_SCHEMA="$schema_verified" E_TABLE_COUNTS="$table_counts_verified" \
  E_STORAGE="$storage_verifier_succeeded" E_HEALTH="$recovered_health_succeeded" \
  E_READY="$recovered_readiness_succeeded" E_AUTH_READ="$recovered_authenticated_read_succeeded" \
  E_METRICS="$recovered_metrics_scrape_succeeded" E_CLEANUP="$cleanup_verified" \
  E_CAPACITY="$capacity_guard_verified" E_OBJECT_VERSIONS="$object_versions_restored" \
  E_PITR="$pitr_wal_replayed" E_ARCHIVE_SIZE="$archive_size_bytes" \
  E_SOURCE_POSTGRES_TLS="$source_postgres_tls_verified" \
  E_SOURCE_POSTGRES_TLS_DSN="$source_postgres_tls_dsn_verified" \
  E_SOURCE_POSTGRES_HBA="$source_postgres_hba_verified" \
  E_SOURCE_POSTGRES_TLS_CONNECTIONS="$source_postgres_tls_connections" \
  E_SOURCE_POSTGRES_TLS_VERSION="$source_postgres_tls_version" \
  E_SOURCE_POSTGRES_TLS_CIPHER="$source_postgres_tls_cipher" \
  E_SOURCE_POSTGRES_TLS_BITS="$source_postgres_tls_bits" \
  E_ARCHIVE_SHA="$archive_sha256" E_SOURCE_SCHEMA="$source_schema_version" \
  E_RESTORED_SCHEMA="$restored_schema_version" E_TABLES="$tables_checked" \
  E_SOURCE_ROWS="$source_total_rows" E_RESTORED_ROWS="$restored_total_rows" \
  E_ROW_SHA="$row_counts_sha256" E_VERIFIER_CHECKED="$verifier_checked" \
  E_VERIFIER_HEALTHY="$verifier_healthy" E_VERIFIER_FINDINGS="$verifier_finding_count" \
  E_VERIFIER_TRUNCATED="$verifier_findings_truncated" E_BACKUP_SECONDS="$backup_elapsed_seconds" \
  E_RESTORE_SECONDS="$restore_elapsed_seconds" E_DISK_USED_BEFORE="$disk_used_percent_before" \
  E_DISK_AVAILABLE_BEFORE="$disk_available_kib_before" E_DISK_USED_AFTER="$disk_used_percent_after" \
  E_DISK_AVAILABLE_AFTER="$disk_available_kib_after" E_MAX_DISK_USED="$max_disk_used_percent" \
  E_MIN_DISK_AVAILABLE="$min_disk_available_kib" E_MAX_ARCHIVE="$max_archive_bytes" \
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
    "schema": "asteria-drive-staging-recovery/v2",
    "status": os.environ["E_STATUS"],
    "last_phase": os.environ["E_PHASE"],
    "started_at": os.environ["E_STARTED_AT"],
    "completed_at": os.environ["E_COMPLETED_AT"],
    "github_run_id": os.environ["E_RUN_ID"],
    "github_run_attempt": os.environ["E_RUN_ATTEMPT"],
    "workflow_sha": os.environ["E_WORKFLOW_SHA"],
    "compose_sha256": os.environ["E_COMPOSE_SHA"],
    "expected_image": os.environ["E_APP_IMAGE"],
    "expected_postgres_image": os.environ["E_POSTGRES_IMAGE"],
    "claim_boundary": "staging-recovery-not-production",
    "backup_catalog_verified": boolean("E_BACKUP_CATALOG"),
    "source_stable_during_backup": boolean("E_SOURCE_STABLE"),
    "restore_succeeded": boolean("E_RESTORE"),
    "schema_verified": boolean("E_SCHEMA"),
    "table_counts_verified": boolean("E_TABLE_COUNTS"),
    "storage_verifier_succeeded": boolean("E_STORAGE"),
    "recovered_health_succeeded": boolean("E_HEALTH"),
    "recovered_readiness_succeeded": boolean("E_READY"),
    "recovered_authenticated_read_succeeded": boolean("E_AUTH_READ"),
    "recovered_metrics_scrape_succeeded": boolean("E_METRICS"),
    "cleanup_verified": boolean("E_CLEANUP"),
    "capacity_guard_verified": boolean("E_CAPACITY"),
    "object_versions_restored": boolean("E_OBJECT_VERSIONS"),
    "pitr_wal_replayed": boolean("E_PITR"),
    "source_postgres_tls_verified": boolean("E_SOURCE_POSTGRES_TLS"),
    "source_postgres_tls_dsn_verified": boolean("E_SOURCE_POSTGRES_TLS_DSN"),
    "source_postgres_hba_verified": boolean("E_SOURCE_POSTGRES_HBA"),
    "source_postgres_tls_connections": integer("E_SOURCE_POSTGRES_TLS_CONNECTIONS"),
    "source_postgres_tls_version": os.environ["E_SOURCE_POSTGRES_TLS_VERSION"],
    "source_postgres_tls_cipher": os.environ["E_SOURCE_POSTGRES_TLS_CIPHER"],
    "source_postgres_tls_bits": integer("E_SOURCE_POSTGRES_TLS_BITS"),
    "backup_archive_size_bytes": integer("E_ARCHIVE_SIZE"),
    "backup_archive_sha256": os.environ["E_ARCHIVE_SHA"],
    "source_schema_version": integer("E_SOURCE_SCHEMA"),
    "restored_schema_version": integer("E_RESTORED_SCHEMA"),
    "tables_checked": integer("E_TABLES"),
    "source_total_rows": integer("E_SOURCE_ROWS"),
    "restored_total_rows": integer("E_RESTORED_ROWS"),
    "row_counts_sha256": os.environ["E_ROW_SHA"],
    "storage_verifier_checked": integer("E_VERIFIER_CHECKED"),
    "storage_verifier_healthy": integer("E_VERIFIER_HEALTHY"),
    "storage_verifier_finding_count": integer("E_VERIFIER_FINDINGS"),
    "storage_verifier_findings_truncated": boolean("E_VERIFIER_TRUNCATED"),
    "backup_elapsed_seconds": integer("E_BACKUP_SECONDS"),
    "restore_elapsed_seconds": integer("E_RESTORE_SECONDS"),
    "disk_used_percent_before": integer("E_DISK_USED_BEFORE"),
    "disk_available_bytes_before": integer("E_DISK_AVAILABLE_BEFORE") * 1024,
    "disk_used_percent_after": integer("E_DISK_USED_AFTER"),
    "disk_available_bytes_after": integer("E_DISK_AVAILABLE_AFTER") * 1024,
    "capacity_max_disk_used_percent": integer("E_MAX_DISK_USED"),
    "capacity_min_disk_available_bytes": integer("E_MIN_DISK_AVAILABLE") * 1024,
    "backup_archive_max_bytes": integer("E_MAX_ARCHIVE"),
}
print(json.dumps(report, indent=2, sort_keys=True))
PY
}

finish() {
  local code="$?"
  trap - EXIT
  cleanup_resources || code=1
  if ! write_evidence; then
    printf 'staging recovery drill could not write evidence\n' >&2
    exit 1
  fi
  exit "$code"
}
trap finish EXIT

phase="source-identity"
[[ -f "$active_compose_file" && ! -L "$active_compose_file" ]] || exit 1
[[ "$(sha256sum "$active_compose_file" | awk '{print $1}')" == "$expected_compose_sha256" ]] || exit 1
source_api_id="$(container_id "$source_api")"
source_postgres_id="$(container_id "$source_postgres")"
source_seaweedfs_id="$(container_id "$source_seaweedfs")"
verify_container "$source_api_id" api "$app_image" none
verify_container "$source_postgres_id" postgres "$postgres_image" healthy
verify_container "$source_seaweedfs_id" seaweedfs "$helper_image" healthy
verify_source_ids

phase="capacity-before"
verify_capacity_scope
disk_available_kib_before="$captured_available_kib"
disk_used_percent_before="$captured_used_percent"
verify_capacity "$disk_used_percent_before" "$disk_available_kib_before" || exit 1
verify_app_secret_metadata || exit 1

phase="prepare-isolation"
[[ ! -e "$work_dir" ]] || exit 1
for name in "$target_container" "$api_container" "$verifier_container" "$auth_container" "$capacity_container"; do
  ! docker container inspect "$name" >/dev/null 2>&1 || exit 1
done
! docker volume inspect "$target_volume" >/dev/null 2>&1 || exit 1
! docker network inspect "$recovery_network" >/dev/null 2>&1 || exit 1
install -d -m 0700 "$work_dir"
work_dir_created="true"
password_file="$work_dir/postgres-password"
database_url_file="$work_dir/database-url"
archive_partial="$work_dir/asteria.dump.partial"
archive="$work_dir/asteria.dump"
source_counts_before_file="$work_dir/source-counts-before.tsv"
source_counts_after_file="$work_dir/source-counts-after.tsv"
target_counts_file="$work_dir/target-counts.tsv"
verifier_file="$work_dir/storage-verifier.json"
target_password="$(openssl rand -hex 24)"
printf '%s\n' "$target_password" >"$password_file"
printf 'postgres://asteria:%s@postgres-recovery:5432/asteria?sslmode=disable\n' "$target_password" >"$database_url_file"
unset target_password
chmod 0400 "$password_file"
chmod 0444 "$database_url_file"

phase="verify-source-postgres-tls"
curl --fail --silent --show-error --max-time 5 http://127.0.0.1:18080/readyz >/dev/null
source_tls_settings="$(docker exec --user postgres "$source_postgres_id" \
  psql -U asteria -d asteria -AtF $'\t' -c \
    "SELECT current_setting('ssl'), current_setting('ssl_min_protocol_version'), current_setting('hba_file')" \
  2>"$work_dir/source-tls-settings.stderr")"
[[ "$source_tls_settings" == $'on\tTLSv1.2\t/var/run/secrets/asteria/pg_hba.conf' ]] || exit 1
source_hba_rules="$(docker exec --user postgres "$source_postgres_id" \
  psql -U asteria -d asteria -Atqc \
    "SELECT COALESCE(string_agg(format('%s|%s|%s|%s|%s|%s|%s', type, array_to_string(database, ','), array_to_string(user_name, ','), COALESCE(address, ''), COALESCE(netmask, ''), auth_method, COALESCE(error, '')), E'\\n' ORDER BY line_number), '') FROM pg_hba_file_rules" \
  2>"$work_dir/source-hba-rules.stderr")"
[[ "$source_hba_rules" == $'local|all|all|||trust|\nhostnossl|all|all|0.0.0.0|0.0.0.0|reject|\nhostnossl|all|all|::|::|reject|\nhostssl|all|all|0.0.0.0|0.0.0.0|scram-sha-256|\nhostssl|all|all|::|::|scram-sha-256|' ]] || exit 1
source_postgres_hba_verified="true"
source_tls_row="$(docker exec --user postgres "$source_postgres_id" \
  psql -U asteria -d asteria -AtF $'\t' -c \
    "SELECT count(*)::text, COALESCE(bool_and(s.ssl), false)::text, COALESCE(min(s.version), ''), COALESCE(min(s.cipher), ''), COALESCE(min(s.bits), 0)::text FROM pg_stat_activity a JOIN pg_stat_ssl s USING (pid) WHERE a.application_name = 'asteria-drive-staging-api'" \
  2>"$work_dir/source-tls-session.stderr")"
IFS=$'\t' read -r source_postgres_tls_connections source_tls_all source_postgres_tls_version \
  source_postgres_tls_cipher source_postgres_tls_bits <<<"$source_tls_row"
[[ "$source_postgres_tls_connections" =~ ^[0-9]+$ && "$source_postgres_tls_connections" -ge 1 ]]
[[ "$source_tls_all" == "true" && "$source_postgres_tls_version" =~ ^TLSv1\.[23]$ ]]
[[ "$source_postgres_tls_cipher" =~ ^[A-Za-z0-9_-]+$ ]]
[[ "$source_postgres_tls_bits" =~ ^[0-9]+$ && "$source_postgres_tls_bits" -ge 128 ]]
source_postgres_tls_verified="true"

phase="backup-source"
verify_source_ids || exit 1
source_schema_before="$(query_schema_version "$source_postgres_id")"
[[ "$source_schema_before" -eq 3 ]] || exit 1
capture_counts "$source_postgres_id" "$source_counts_before_file"
backup_started_epoch="$(date +%s)"
if ! timeout 180 docker exec --user postgres "$source_postgres_id" \
  pg_dump --username asteria --dbname asteria --format=custom --compress=9 \
  --no-owner --no-acl 2>"$work_dir/backup.stderr" |
  head -c "$((max_archive_bytes + 1))" >"$archive_partial"; then
  archive_size_bytes="$(stat -c '%s' "$archive_partial" 2>/dev/null || printf '0')"
  if [[ "$archive_size_bytes" =~ ^[0-9]+$ && "$archive_size_bytes" -gt "$max_archive_bytes" ]]; then
    printf 'staging recovery backup exceeded the archive size limit\n' >&2
  else
    printf 'staging recovery backup failed\n' >&2
  fi
  exit 1
fi
archive_size_bytes="$(stat -c '%s' "$archive_partial")"
[[ "$archive_size_bytes" =~ ^[0-9]+$ ]] || exit 1
(( archive_size_bytes > 0 && archive_size_bytes <= max_archive_bytes )) || exit 1
mv -- "$archive_partial" "$archive"
archive_sha256="$(sha256sum "$archive" | awk '{print $1}')"
[[ "$archive_sha256" =~ ^[0-9a-f]{64}$ ]] || exit 1
if ! timeout 60 docker exec -i --user postgres "$source_postgres_id" \
  pg_restore --list <"$archive" >"$work_dir/catalog.txt" 2>"$work_dir/catalog.stderr"; then
  printf 'staging recovery archive catalog verification failed\n' >&2
  exit 1
fi
[[ -s "$work_dir/catalog.txt" ]] || exit 1
backup_catalog_verified="true"
verify_source_ids || exit 1
source_schema_version="$(query_schema_version "$source_postgres_id")"
[[ "$source_schema_version" -eq "$source_schema_before" && "$source_schema_version" -eq 3 ]] || exit 1
capture_counts "$source_postgres_id" "$source_counts_after_file"
verify_source_ids || exit 1
verify_container "$source_api_id" api "$app_image" none || exit 1
verify_container "$source_postgres_id" postgres "$postgres_image" healthy || exit 1
verify_container "$source_seaweedfs_id" seaweedfs "$helper_image" healthy || exit 1
cmp -s "$source_counts_before_file" "$source_counts_after_file" || {
  printf 'staging source changed during backup; retry the drill\n' >&2
  exit 1
}
source_stable_during_backup="true"
source_total_rows="$(count_total "$source_counts_after_file")"
backup_elapsed_seconds=$(( $(date +%s) - backup_started_epoch ))

phase="start-isolated-postgres"
docker network create --internal \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  "$recovery_network" >/dev/null
network_created="true"
resource_labels_match network "$recovery_network" || { network_created="false"; exit 1; }
docker volume create \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  "$target_volume" >/dev/null
volume_created="true"
resource_labels_match volume "$target_volume" || { volume_created="false"; exit 1; }
[[ "$(docker volume inspect --format '{{.Driver}}' "$target_volume")" == "local" ]] || exit 1
case "$(docker volume inspect --format '{{json .Options}}' "$target_volume")" in
  null|'{}') ;;
  *) exit 1 ;;
esac
verify_capacity_scope || exit 1
verify_volume_capacity "$target_volume" || exit 1
docker network connect --alias seaweedfs-recovery "$recovery_network" "$source_seaweedfs_id"
seaweedfs_connected="true"
resource_labels_match network "$recovery_network" || exit 1
resource_labels_match volume "$target_volume" || exit 1
docker run -d --pull never \
  --name "$target_container" --network "$recovery_network" --network-alias postgres-recovery \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  --read-only --security-opt no-new-privileges:true --pids-limit 256 \
  --memory 768m --cpus 1.0 --log-driver none \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --tmpfs /run/postgresql:rw,noexec,nosuid,size=16m \
  --mount "type=volume,source=$target_volume,target=/var/lib/postgresql/data" \
  --mount "type=bind,source=$password_file,target=/run/recovery/postgres-password,readonly" \
  --env POSTGRES_DB=asteria --env POSTGRES_USER=asteria \
  --env POSTGRES_PASSWORD_FILE=/run/recovery/postgres-password \
  "$postgres_image" >/dev/null
target_created="true"
resource_labels_match container "$target_container" || exit 1
verify_image_identity "$target_container" "$postgres_image" || exit 1
target_ready="false"
for _ in $(seq 1 60); do
  if docker exec --user postgres "$target_container" pg_isready -q --username asteria --dbname asteria; then
    target_ready="true"
    break
  fi
  sleep 1
done
[[ "$target_ready" == "true" ]] || { printf 'isolated PostgreSQL did not become ready\n' >&2; exit 1; }

phase="restore-metadata"
restore_started_epoch="$(date +%s)"
restore_capacity_failed="false"
timeout 180 docker exec -i --user postgres "$target_container" \
  pg_restore --username asteria --dbname asteria --no-owner --no-acl \
  --exit-on-error --single-transaction <"$archive" 2>"$work_dir/restore.stderr" &
restore_pid="$!"
while kill -0 "$restore_pid" >/dev/null 2>&1; do
  sleep 2
  if ! verify_capacity_scope || ! verify_volume_capacity "$target_volume"; then
    restore_capacity_failed="true"
    kill -TERM "$restore_pid" >/dev/null 2>&1 || true
    break
  fi
done
if ! wait "$restore_pid" || [[ "$restore_capacity_failed" == "true" ]]; then
  printf 'isolated PostgreSQL restore failed\n' >&2
  exit 1
fi
verify_capacity_scope || exit 1
verify_volume_capacity "$target_volume" || exit 1
restore_succeeded="true"
capture_counts "$target_container" "$target_counts_file"
cmp -s "$source_counts_after_file" "$target_counts_file" || {
  printf 'restored table counts do not match the backup checkpoint\n' >&2
  exit 1
}
table_counts_verified="true"
tables_checked="${#tables[@]}"
restored_total_rows="$(count_total "$target_counts_file")"
[[ "$restored_total_rows" -eq "$source_total_rows" ]] || exit 1
row_counts_sha256="$(sha256sum "$target_counts_file" | awk '{print $1}')"
[[ "$row_counts_sha256" =~ ^[0-9a-f]{64}$ ]] || exit 1
restored_schema_version="$(query_schema_version "$target_container")"
[[ "$restored_schema_version" -eq "$source_schema_version" && "$restored_schema_version" -eq 3 ]] || exit 1
schema_verified="true"

common_environment=(
  --env ASTERIA_ENV=development
  --env ASTERIA_AUTH_MODE=trusted-dev
  --env ASTERIA_TRUSTED_TOKENS_JSON_FILE=/var/run/secrets/asteria/trusted-tokens.json
  --env ASTERIA_CURSOR_HMAC_KEY_FILE=/var/run/secrets/asteria/cursor-hmac-key
  --env ASTERIA_METADATA_DRIVER=postgres
  --env ASTERIA_DATABASE_URL_FILE=/var/run/recovery/database-url
  --env ASTERIA_AUTO_MIGRATE=false
  --env ASTERIA_STORAGE_DRIVER=s3
  --env ASTERIA_S3_ENDPOINT=http://seaweedfs-recovery:8333
  --env ASTERIA_S3_REGION=us-east-1
  --env ASTERIA_S3_BUCKET=asteria-drive-staging
  --env ASTERIA_S3_ACCESS_KEY_ID_FILE=/var/run/secrets/asteria/s3-access-key-id
  --env ASTERIA_S3_SECRET_ACCESS_KEY_FILE=/var/run/secrets/asteria/s3-secret-access-key
  --env ASTERIA_S3_PATH_STYLE=true
  --env ASTERIA_S3_AUTO_CREATE_BUCKET=false
  --env ASTERIA_S3_CHECKSUM_HEADERS=false
  --env ASTERIA_MAINTENANCE_ENABLED=false
)
common_mounts=(
  --mount type=volume,source=asteria-drive-staging-app-secrets,target=/var/run/secrets/asteria,readonly
  --mount "type=bind,source=$database_url_file,target=/var/run/recovery/database-url,readonly"
)

phase="verify-restored-storage"
verifier_created="true"
if ! timeout 120 docker run --pull never --name "$verifier_container" --network "$recovery_network" \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  --user 65532:65532 --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --pids-limit 64 --memory 256m --cpus 0.5 \
  --log-driver none "${common_mounts[@]}" "${common_environment[@]}" \
  --entrypoint /usr/local/bin/asteria-verify-storage "$app_image" \
  -batch-size 500 -concurrency 8 -max-findings 100 -timeout 2m \
  >"$verifier_file" 2>"$work_dir/verifier.stderr"; then
  printf 'restored storage verification failed\n' >&2
  exit 1
fi
resource_labels_match container "$verifier_container" || exit 1
verifier_values="$(python3 - "$verifier_file" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    report = json.load(handle)
if report.get("verified") is not True:
    raise SystemExit("storage report is not verified")
checked = report.get("checked")
healthy = report.get("healthy")
counts = report.get("finding_counts")
findings = report.get("findings")
truncated = report.get("findings_truncated")
if type(checked) is not int or checked < 1 or healthy != checked:
    raise SystemExit("storage report counts are invalid")
if counts not in ({}, None) or findings not in ([], None) or truncated is not False:
    raise SystemExit("storage report contains findings")
print(f"{checked}\t{healthy}\t0\tfalse")
PY
)" || exit 1
IFS=$'\t' read -r verifier_checked verifier_healthy verifier_finding_count verifier_findings_truncated <<<"$verifier_values"
[[ "$verifier_checked" =~ ^[0-9]+$ && "$verifier_healthy" == "$verifier_checked" ]] || exit 1
storage_verifier_succeeded="true"

phase="start-recovered-api"
docker run -d --pull never --name "$api_container" \
  --network "$recovery_network" --network-alias api-recovery \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  --user 65532:65532 --read-only --init --cap-drop ALL \
  --security-opt no-new-privileges:true --pids-limit 128 --memory 512m --cpus 1.0 \
  --log-driver none "${common_mounts[@]}" "${common_environment[@]}" \
  --env ASTERIA_SERVER_ADDRESS=0.0.0.0:8080 \
  --env ASTERIA_METRICS_ADDRESS=0.0.0.0:9090 \
  "$app_image" >/dev/null
api_created="true"
resource_labels_match container "$api_container" || exit 1
verify_image_identity "$api_container" "$app_image" || exit 1
api_ready="false"
for _ in $(seq 1 60); do
  if docker exec "$source_seaweedfs_id" wget -q -T 2 -O /dev/null http://api-recovery:8080/healthz; then
    api_ready="true"
    break
  fi
  sleep 1
done
[[ "$api_ready" == "true" ]] || { printf 'recovered API did not become healthy\n' >&2; exit 1; }
recovered_health_succeeded="true"
docker exec "$source_seaweedfs_id" wget -q -T 5 -O /dev/null http://api-recovery:8080/readyz
recovered_readiness_succeeded="true"
docker exec "$source_seaweedfs_id" wget -q -T 5 -O - http://api-recovery:9090/metrics |
  awk '/^asteria_http_requests_total([ {]|$)/ { found = 1 } END { exit(found ? 0 : 1) }'
recovered_metrics_scrape_succeeded="true"

phase="authenticated-read"
auth_created="true"
if ! docker run --pull never --name "$auth_container" --network "$recovery_network" \
  --label "com.baicie.asteria.boundary=$resource_boundary" \
  --label "com.baicie.asteria.run-id=$run_id" \
  --label "com.baicie.asteria.run-attempt=$run_attempt" \
  --user 65532:65532 \
  --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true --pids-limit 16 --memory 32m --cpus 0.25 \
  --log-driver none --mount type=volume,source=asteria-drive-staging-app-secrets,target=/secrets,readonly \
  --entrypoint /bin/sh "$helper_image" -ec '
    document="$(cat /secrets/trusted-tokens.json)"
    token="${document#*\"}"
    token="${token%%\"*}"
    [ "${#token}" -ge 32 ] && [ "${#token}" -le 256 ] || exit 1
    case "$token" in *[!A-Za-z0-9._~-]*) exit 1 ;; esac
    response="$(wget -q -T 5 -O - --header="Authorization: Bearer $token" \
      http://api-recovery:8080/api/v1/tenant)"
    root_id="$(printf "%s" "$response" | sed -n "s/.*\"root_directory_id\"[[:space:]]*:[[:space:]]*\"\([^\"[:space:]]*\)\".*/\1/p")"
    [ "${#root_id}" -eq 36 ] || exit 1
    case "$root_id" in ????????-????-????-????-????????????) ;; *) exit 1 ;; esac
    case "$root_id" in *[!0-9a-f-]*) exit 1 ;; esac
  ' >/dev/null; then
  printf 'recovered authenticated read failed\n' >&2
  exit 1
fi
resource_labels_match container "$auth_container" || exit 1
recovered_authenticated_read_succeeded="true"
restore_elapsed_seconds=$(( $(date +%s) - restore_started_epoch ))

phase="cleanup"
cleanup_resources || { printf 'staging recovery cleanup failed\n' >&2; exit 1; }
verify_capacity_scope
disk_available_kib_after="$captured_available_kib"
disk_used_percent_after="$captured_used_percent"
verify_capacity "$disk_used_percent_after" "$disk_available_kib_after" || exit 1
capacity_guard_verified="true"

phase="complete"
status="success"
