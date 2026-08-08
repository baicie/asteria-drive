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
release_tag="v0.1.0"
source_commit="8878d9eaaf88973c522a4f4742ea960acd63d503"
expected_image="ghcr.io/baicie/asteria-drive@sha256:f5da244cba2055764a8caae7b9e9a752cc8f07356c0d7ae6397a6a7992e0cccc"
helper_image="chrislusf/seaweedfs:3.85@sha256:49312939c00c01e5ee6afbd7d728b18027821d3764c35a797a72acd4fdf3296a"
expected_compose_sha256="37a5922a4058632813ba893da65e5f975e0142612732d02e75ef7a922f4dfcf6"
tenant_id="11111111-1111-4111-8111-111111111111"

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
smoke_dir=""

snapshot() {
  local prefix="$1" load memory disk
  load="$(awk '{print $1}' /proc/loadavg)"
  memory="$(awk '/MemTotal:/ {t=$2} /MemAvailable:/ {a=$2} END {printf "%.2f", (t-a)*100/t}' /proc/meminfo)"
  disk="$(df -P / | awk 'NR==2 {gsub(/%/, "", $5); print $5}')"
  printf -v "load_${prefix}" '%s' "$load"
  printf -v "memory_used_${prefix}" '%s' "$memory"
  printf -v "disk_used_${prefix}" '%s' "$disk"
}

write_evidence() {
  local completed_at tmp_path
  completed_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  tmp_path="${evidence_path}.tmp"
  install -d -m 0750 "$(dirname "$evidence_path")"
  E_STATUS="$status" \
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
  E_VERIFIER_CHECKED="$verifier_checked" \
  E_SCHEMA_BEFORE="$migration_schema_before" \
  E_SCHEMA_AFTER="$migration_schema_after" \
  E_LOAD_BEFORE="$load_before" \
  E_LOAD_AFTER="$load_after" \
  E_MEMORY_BEFORE="$memory_used_before" \
  E_MEMORY_AFTER="$memory_used_after" \
  E_DISK_BEFORE="$disk_used_before" \
  E_DISK_AFTER="$disk_used_after" \
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
    "schema": "asteria-drive-staging-deployment/v1",
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
    "storage_verifier_checked": int(os.environ["E_VERIFIER_CHECKED"]),
    "migration_schema_before": int(os.environ["E_SCHEMA_BEFORE"]),
    "migration_schema_after": int(os.environ["E_SCHEMA_AFTER"]),
    "load1_before": float(os.environ["E_LOAD_BEFORE"]),
    "load1_after": float(os.environ["E_LOAD_AFTER"]),
    "memory_used_percent_before": float(os.environ["E_MEMORY_BEFORE"]),
    "memory_used_percent_after": float(os.environ["E_MEMORY_AFTER"]),
    "disk_used_percent_before": int(os.environ["E_DISK_BEFORE"]),
    "disk_used_percent_after": int(os.environ["E_DISK_AFTER"]),
    "exposure": "loopback-bindings-verified",
    "secret_source": "server-managed-docker-volumes",
    "claim_boundary": "staging-not-production",
}
with open(sys.argv[1], "w", encoding="utf-8") as handle:
    json.dump(payload, handle, indent=2, sort_keys=True)
    handle.write("\n")
PY
  chmod 0600 "$tmp_path"
  mv -f -- "$tmp_path" "$evidence_path"
}

cleanup_smoke() {
  [[ -n "$smoke_dir" ]] || return 0
  local parent
  parent="$(dirname "$evidence_path")"
  [[ "$smoke_dir" =~ ^${parent//./\\.}/smoke\.[A-Za-z0-9]{8}$ ]] || return 1
  rm -rf -- "$smoke_dir"
  smoke_dir=""
}

finish() {
  local code="$?" evidence_code=0
  trap - EXIT
  set +e
  snapshot after
  if ! cleanup_smoke; then
    printf 'could not remove staging smoke files\n' >&2
    code=1
    phase="cleanup-smoke"
  fi
  if [[ -f "$compose_file" && "$compose_file" != "$active_compose_file" ]]; then
    rm -f -- "$compose_file" || code=1
  fi
  if [[ "$code" -eq 0 ]]; then
    status="success"
    phase="complete"
  fi
  write_evidence
  evidence_code="$?"
  if [[ "$evidence_code" -ne 0 ]]; then
    printf 'could not write deployment evidence\n' >&2
    code=1
  fi
  exit "$code"
}
trap finish EXIT

snapshot before

phase="validate-prerequisites"
for command in cmp curl docker python3 sha256sum; do
  command -v "$command" >/dev/null || { printf '%s is required\n' "$command" >&2; exit 1; }
done
docker compose version >/dev/null
for volume in \
  asteria-drive-staging-app-secrets \
  asteria-drive-staging-postgres-secrets \
  asteria-drive-staging-seaweedfs-secrets; do
  docker volume inspect "$volume" >/dev/null
done

phase="validate-compose"
install -d -m 0750 "$deploy_root"
install -m 0644 "$compose_source" "$compose_file"
compose_sha256="$(sha256sum "$compose_file" | awk '{print $1}')"
[[ "$compose_sha256" == "$expected_compose_sha256" ]] || { printf 'staging Compose digest is not approved\n' >&2; exit 1; }
grep -Fq "$expected_image" "$compose_file"
grep -Fq '127.0.0.1:18080:8080' "$compose_file"
grep -Fq '127.0.0.1:18333:8333' "$compose_file"
grep -Fq '127.0.0.1:19090:9090' "$compose_file"
if grep -Eq 'image:.*:latest|sha256:0{64}|ASTERIA_ENV:[[:space:]]*production' "$compose_file"; then
  printf 'staging Compose contains a forbidden mutable, placeholder, or production value\n' >&2
  exit 1
fi
docker compose -p "$project" -f "$compose_file" --profile tools config --quiet

api_id="$(docker compose -p "$project" -f "$compose_file" ps -q api 2>/dev/null || true)"
if [[ -n "$api_id" ]]; then
  previous_image="$(docker inspect --format '{{.Config.Image}}' "$api_id")"
fi

phase="pull-images"
docker compose -p "$project" -f "$compose_file" --profile tools pull --quiet
version_output="$(docker run --rm "$expected_image" --version)"
grep -Fq "$release_tag" <<<"$version_output"
grep -Fq "$source_commit" <<<"$version_output"
binary_version_verified="true"

phase="start-dependencies"
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

phase="verify-runtime-identity"
api_id="$(docker compose -p "$project" -f "$compose_file" ps -q api)"
deployed_image_ref="$(docker inspect --format '{{.Config.Image}}' "$api_id")"
deployed_image_id="$(docker inspect --format '{{.Image}}' "$api_id")"
[[ "$deployed_image_ref" == "$expected_image" ]] || { printf 'running API image does not match the approved digest\n' >&2; exit 1; }
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

smoke_dir="$(mktemp -d "$(dirname "$evidence_path")/smoke.XXXXXXXX")"

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

phase="activate-compose"
mv -f -- "$compose_file" "$active_compose_file"
compose_file="$active_compose_file"
