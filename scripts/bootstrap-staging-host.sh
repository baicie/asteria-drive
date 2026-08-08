#!/usr/bin/env bash
set -Eeuo pipefail

deploy_user="${ASTERIA_DEPLOY_USER:-asteria-deploy}"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
public_key_b64="${ASTERIA_DEPLOY_PUBLIC_KEY_B64:-}"
dispatcher_path="/usr/local/libexec/asteria-staging-dispatch"
dispatcher_config="/etc/asteria-drive/staging-dispatch.conf"
expected_compose_sha256="a031974150a7724a17be68d58cb008305bd45374b8e04dfcd3dd2b0c233645dc"
expected_deploy_script_sha256="da2e89abea4154f612f204bf61ae9097cf26a9f654149fb041bf0274aff75e35"

if [[ "$(id -u)" -ne 0 ]]; then
  printf 'bootstrap-staging-host.sh must run as root\n' >&2
  exit 1
fi
if [[ -z "$public_key_b64" ]]; then
  printf 'ASTERIA_DEPLOY_PUBLIC_KEY_B64 is required\n' >&2
  exit 1
fi
if [[ ! "$deploy_user" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]]; then
  printf 'ASTERIA_DEPLOY_USER is invalid\n' >&2
  exit 1
fi
if [[ ! "$deploy_root" =~ ^/opt/asteria-drive/[A-Za-z0-9._-]+$ ]]; then
  printf 'ASTERIA_DEPLOY_ROOT is outside the allowed path\n' >&2
  exit 1
fi
if ! command -v docker >/dev/null || ! docker compose version >/dev/null 2>&1; then
  printf 'Docker Engine and Docker Compose are required\n' >&2
  exit 1
fi
for command in base64 openssl python3 sha256sum; do
  command -v "$command" >/dev/null || { printf '%s is required\n' "$command" >&2; exit 1; }
done

public_key="$(printf '%s' "$public_key_b64" | base64 -d)"
public_key="${public_key%$'\r'}"
if [[ ! "$public_key" =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+\ [A-Za-z0-9._@-]+$ ]]; then
  printf 'the deploy public key is invalid\n' >&2
  exit 1
fi

if ! id "$deploy_user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$deploy_user"
fi
usermod --append --groups docker "$deploy_user"
deploy_home="$(getent passwd "$deploy_user" | cut -d: -f6)"
deploy_group="$(id -gn "$deploy_user")"
install -d -m 0750 -o "$deploy_user" -g "$deploy_group" "$deploy_root"

app_volume="asteria-drive-staging-app-secrets"
postgres_volume="asteria-drive-staging-postgres-secrets"
seaweedfs_volume="asteria-drive-staging-seaweedfs-secrets"
for volume in "$app_volume" "$postgres_volume" "$seaweedfs_volume"; do
  docker volume create "$volume" >/dev/null
done
app_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$app_volume")"
postgres_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$postgres_volume")"
seaweedfs_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$seaweedfs_volume")"

expected=(
  "$app_dir/database-url"
  "$app_dir/cursor-hmac-key"
  "$app_dir/trusted-tokens.json"
  "$app_dir/s3-access-key-id"
  "$app_dir/s3-secret-access-key"
  "$postgres_dir/postgres-password"
  "$seaweedfs_dir/s3.json"
)
present=0
for path in "${expected[@]}"; do
  [[ -f "$path" ]] && present=$((present + 1))
done
if [[ "$present" -ne 0 && "$present" -ne "${#expected[@]}" ]]; then
  printf 'staging secret volumes are only partially initialized; refusing to rotate implicitly\n' >&2
  exit 1
fi

if [[ "$present" -eq 0 ]]; then
  postgres_password="$(openssl rand -hex 24)"
  cursor_key="$(openssl rand -hex 48)"
  trusted_token="$(openssl rand -hex 32)"
  s3_access_key="asteria$(openssl rand -hex 10)"
  s3_secret_key="$(openssl rand -hex 32)"

  umask 077
  printf 'postgres://asteria:%s@postgres:5432/asteria?sslmode=disable\n' "$postgres_password" >"$app_dir/database-url"
  printf '%s\n' "$cursor_key" >"$app_dir/cursor-hmac-key"
  printf '{"%s":{"tenant_id":"11111111-1111-4111-8111-111111111111","principal_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","tenant_name":"Asteria staging"}}\n' "$trusted_token" >"$app_dir/trusted-tokens.json"
  printf '%s\n' "$s3_access_key" >"$app_dir/s3-access-key-id"
  printf '%s\n' "$s3_secret_key" >"$app_dir/s3-secret-access-key"
  printf '%s\n' "$postgres_password" >"$postgres_dir/postgres-password"
  cat >"$seaweedfs_dir/s3.json" <<EOF
{"identities":[{"name":"asteria-staging","credentials":[{"accessKey":"$s3_access_key","secretKey":"$s3_secret_key"}],"actions":["Admin","Read","List","Tagging","Write"]}]}
EOF

  chown -R 65532:65532 "$app_dir"
  chmod 0400 "$app_dir"/*
  chown -R root:root "$postgres_dir" "$seaweedfs_dir"
  chmod 0400 "$postgres_dir/postgres-password" "$seaweedfs_dir/s3.json"
fi

require_metadata() {
  local path="$1" expected_owner="$2" actual
  [[ -s "$path" && ! -L "$path" ]] || { printf 'invalid staging secret file: %s\n' "$path" >&2; exit 1; }
  actual="$(stat -c '%u:%g:%a' "$path")"
  [[ "$actual" == "$expected_owner:400" ]] || {
    printf 'unsafe staging secret metadata for %s\n' "$path" >&2
    exit 1
  }
}
for path in \
  "$app_dir/database-url" \
  "$app_dir/cursor-hmac-key" \
  "$app_dir/trusted-tokens.json" \
  "$app_dir/s3-access-key-id" \
  "$app_dir/s3-secret-access-key"; do
  require_metadata "$path" "65532:65532"
done
require_metadata "$postgres_dir/postgres-password" "0:0"
require_metadata "$seaweedfs_dir/s3.json" "0:0"

python3 - \
  "$app_dir/database-url" "$app_dir/cursor-hmac-key" "$app_dir/trusted-tokens.json" \
  "$app_dir/s3-access-key-id" "$app_dir/s3-secret-access-key" \
  "$postgres_dir/postgres-password" "$seaweedfs_dir/s3.json" <<'PY'
import json
import sys
import urllib.parse

database_path, cursor_path, tokens_path, access_path, secret_path, password_path, s3_path = sys.argv[1:]

def read(path):
    with open(path, encoding="utf-8") as handle:
        value = handle.read().strip()
    if not value:
        raise SystemExit("staging secret file is empty")
    return value

password = read(password_path)
database = urllib.parse.urlsplit(read(database_path))
if database.scheme not in {"postgres", "postgresql"} or database.hostname != "postgres":
    raise SystemExit("staging database URL is invalid")
if database.username != "asteria" or urllib.parse.unquote(database.password or "") != password:
    raise SystemExit("staging database credentials are inconsistent")
if len(read(cursor_path)) < 32:
    raise SystemExit("staging cursor key is too short")

tokens = json.loads(read(tokens_path))
if len(tokens) != 1:
    raise SystemExit("staging trusted token document must contain one principal")
token, principal = next(iter(tokens.items()))
if len(token) < 32 or principal.get("tenant_id") != "11111111-1111-4111-8111-111111111111":
    raise SystemExit("staging trusted token document is invalid")

access_key = read(access_path)
secret_key = read(secret_path)
s3 = json.loads(read(s3_path))
identities = s3.get("identities")
if not isinstance(identities, list) or len(identities) != 1:
    raise SystemExit("staging S3 identity document is invalid")
credentials = identities[0].get("credentials")
if not isinstance(credentials, list) or len(credentials) != 1:
    raise SystemExit("staging S3 credentials are invalid")
if credentials[0].get("accessKey") != access_key or credentials[0].get("secretKey") != secret_key:
    raise SystemExit("staging S3 credentials are inconsistent")
required_actions = {"Admin", "Read", "List", "Tagging", "Write"}
if set(identities[0].get("actions", [])) != required_actions:
    raise SystemExit("staging S3 actions are invalid")
PY

dispatcher_tmp="$(mktemp /tmp/asteria-staging-dispatch.XXXXXXXX)"
config_tmp="$(mktemp /tmp/asteria-staging-dispatch-config.XXXXXXXX)"
cleanup_bootstrap() {
  rm -f -- "$dispatcher_tmp" "$config_tmp"
}
trap cleanup_bootstrap EXIT

cat >"$dispatcher_tmp" <<'DISPATCHER'
#!/usr/bin/env bash
set -Eeuo pipefail

source /etc/asteria-drive/staging-dispatch.conf

fail() {
  printf 'staging deploy command rejected\n' >&2
  exit 1
}

[[ -n "${SSH_ORIGINAL_COMMAND:-}" ]] || fail
read -r -a fields <<<"$SSH_ORIGINAL_COMMAND"
[[ "${#fields[@]}" -ge 1 ]] || fail
action="${fields[0]}"

validate_run() {
  [[ "${#fields[@]}" -ge 3 ]] || fail
  run_id="${fields[1]}"
  run_attempt="${fields[2]}"
  [[ "$run_id" =~ ^[0-9]{1,20}$ && "$run_attempt" =~ ^[0-9]{1,6}$ ]] || fail
  run_dir="/tmp/asteria-staging.${run_id}.${run_attempt}"
}

require_run_dir() {
  [[ -d "$run_dir" && ! -L "$run_dir" && -O "$run_dir" ]] || fail
}

receive_file() {
  local destination="$1" expected_sha="$2" limit="$3" temporary size actual_sha
  temporary="${destination}.tmp"
  rm -f -- "$temporary"
  umask 077
  head -c "$((limit + 1))" >"$temporary"
  size="$(stat -c '%s' "$temporary")"
  [[ "$size" -gt 0 && "$size" -le "$limit" ]] || fail
  actual_sha="$(sha256sum "$temporary" | awk '{print $1}')"
  [[ "$actual_sha" == "$expected_sha" ]] || fail
  mv -f -- "$temporary" "$destination"
  chmod 0600 "$destination"
}

case "$action" in
  prepare)
    [[ "${#fields[@]}" -eq 3 ]] || fail
    validate_run
    [[ ! -e "$run_dir" ]] || fail
    install -d -m 0700 "$run_dir"
    ;;
  upload-compose)
    [[ "${#fields[@]}" -eq 3 ]] || fail
    validate_run
    require_run_dir
    receive_file "$run_dir/compose.yaml" "$compose_sha256" 262144
    ;;
  upload-script)
    [[ "${#fields[@]}" -eq 3 ]] || fail
    validate_run
    require_run_dir
    receive_file "$run_dir/deploy-staging.sh" "$deploy_script_sha256" 524288
    ;;
  deploy)
    [[ "${#fields[@]}" -eq 4 ]] || fail
    validate_run
    require_run_dir
    workflow_sha="${fields[3]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ -f "$run_dir/compose.yaml" && ! -L "$run_dir/compose.yaml" ]] || fail
    [[ -f "$run_dir/deploy-staging.sh" && ! -L "$run_dir/deploy-staging.sh" ]] || fail
    [[ "$(sha256sum "$run_dir/compose.yaml" | awk '{print $1}')" == "$compose_sha256" ]] || fail
    [[ "$(sha256sum "$run_dir/deploy-staging.sh" | awk '{print $1}')" == "$deploy_script_sha256" ]] || fail
    exec env ASTERIA_DEPLOY_ROOT="$deploy_root" bash "$run_dir/deploy-staging.sh" \
      "$run_dir/compose.yaml" "$run_dir/deployment-evidence.json" \
      "$run_id" "$run_attempt" "$workflow_sha"
    ;;
  fetch)
    [[ "${#fields[@]}" -eq 4 ]] || fail
    validate_run
    require_run_dir
    case "${fields[3]}" in
      deployment-evidence) artifact="$run_dir/deployment-evidence.json" ;;
      storage-verifier) artifact="$run_dir/storage-verifier.json" ;;
      *) fail ;;
    esac
    [[ -f "$artifact" && ! -L "$artifact" ]] || fail
    [[ "$(stat -c '%s' "$artifact")" -le 2097152 ]] || fail
    exec cat -- "$artifact"
    ;;
  cleanup)
    [[ "${#fields[@]}" -eq 3 ]] || fail
    validate_run
    require_run_dir
    rm -rf -- "$run_dir"
    ;;
  *)
    fail
    ;;
esac
DISPATCHER

{
  printf 'compose_sha256=%q\n' "$expected_compose_sha256"
  printf 'deploy_script_sha256=%q\n' "$expected_deploy_script_sha256"
  printf 'deploy_root=%q\n' "$deploy_root"
} >"$config_tmp"

install -d -m 0755 /usr/local/libexec
install -d -m 0755 /etc/asteria-drive
install -m 0755 -o root -g root "$dispatcher_tmp" "$dispatcher_path"
install -m 0644 -o root -g root "$config_tmp" "$dispatcher_config"
install -d -m 0700 -o "$deploy_user" -g "$deploy_group" "$deploy_home/.ssh"
printf 'restrict,command="%s" %s\n' "$dispatcher_path" "$public_key" >"$deploy_home/.ssh/authorized_keys"
chown "$deploy_user:$deploy_group" "$deploy_home/.ssh/authorized_keys"
chmod 0600 "$deploy_home/.ssh/authorized_keys"

trap - EXIT
cleanup_bootstrap
printf 'staging host bootstrap is ready\n'
