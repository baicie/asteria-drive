#!/usr/bin/env bash
set -Eeuo pipefail

deploy_user="${ASTERIA_DEPLOY_USER:-asteria-deploy}"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
public_key_b64="${ASTERIA_DEPLOY_PUBLIC_KEY_B64:-}"
monitor_source_b64="${ASTERIA_STAGING_MONITOR_B64:-}"
recovery_source_b64="${ASTERIA_STAGING_RECOVERY_B64:-}"
dispatcher_path="/usr/local/libexec/asteria-staging-dispatch"
dispatcher_config="/etc/asteria-drive/staging-dispatch.conf"
monitor_path="/usr/local/libexec/asteria-staging-monitor"
recovery_path="/usr/local/libexec/asteria-staging-recovery"
expected_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"
expected_deploy_script_sha256="cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044"
expected_monitor_script_sha256="3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279"
expected_recovery_script_sha256="3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a"

if [[ "$(id -u)" -ne 0 ]]; then
  printf 'bootstrap-staging-host.sh must run as root\n' >&2
  exit 1
fi
if [[ -z "$public_key_b64" ]]; then
  printf 'ASTERIA_DEPLOY_PUBLIC_KEY_B64 is required\n' >&2
  exit 1
fi
if [[ -z "$monitor_source_b64" || ! "$monitor_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#monitor_source_b64}" -gt 1048576 ]]; then
  printf 'ASTERIA_STAGING_MONITOR_B64 is invalid\n' >&2
  exit 1
fi
if [[ -z "$recovery_source_b64" || ! "$recovery_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#recovery_source_b64}" -gt 2097152 ]]; then
  printf 'ASTERIA_STAGING_RECOVERY_B64 is invalid\n' >&2
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
for command in awk base64 cmp grep openssl python3 sha256sum; do
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
pki_root="/etc/asteria-drive/staging-postgres-pki"
pki_issuer="$pki_root/issuer"
for volume in "$app_volume" "$postgres_volume" "$seaweedfs_volume"; do
  docker volume create "$volume" >/dev/null
done
app_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$app_volume")"
postgres_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$postgres_volume")"
seaweedfs_dir="$(docker volume inspect --format '{{.Mountpoint}}' "$seaweedfs_volume")"

core_expected=(
  "$app_dir/database-url"
  "$app_dir/cursor-hmac-key"
  "$app_dir/trusted-tokens.json"
  "$app_dir/s3-access-key-id"
  "$app_dir/s3-secret-access-key"
  "$postgres_dir/postgres-password"
  "$seaweedfs_dir/s3.json"
)
core_present=0
for path in "${core_expected[@]}"; do
  [[ -f "$path" ]] && core_present=$((core_present + 1))
done
if [[ "$core_present" -ne 0 && "$core_present" -ne "${#core_expected[@]}" ]]; then
  printf 'staging secret volumes are only partially initialized; refusing to rotate implicitly\n' >&2
  exit 1
fi

if [[ "$core_present" -eq 0 ]]; then
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

  chown 65532:65532 \
    "$app_dir/database-url" "$app_dir/cursor-hmac-key" "$app_dir/trusted-tokens.json" \
    "$app_dir/s3-access-key-id" "$app_dir/s3-secret-access-key"
  chmod 0400 \
    "$app_dir/database-url" "$app_dir/cursor-hmac-key" "$app_dir/trusted-tokens.json" \
    "$app_dir/s3-access-key-id" "$app_dir/s3-secret-access-key"
  chown root:root "$postgres_dir/postgres-password" "$seaweedfs_dir/s3.json"
  chmod 0400 "$postgres_dir/postgres-password" "$seaweedfs_dir/s3.json"
fi

require_metadata() {
  local path="$1" expected_owner="$2" expected_mode="$3" actual
  [[ -s "$path" && ! -L "$path" ]] || { printf 'invalid staging secret file: %s\n' "$path" >&2; exit 1; }
  actual="$(stat -c '%u:%g:%a' "$path")"
  [[ "$actual" == "$expected_owner:$expected_mode" ]] || {
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
  require_metadata "$path" "65532:65532" "400"
done
require_metadata "$postgres_dir/postgres-password" "0:0" "400"
require_metadata "$seaweedfs_dir/s3.json" "0:0" "400"

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

tls_expected=(
  "$app_dir/database-url-tls"
  "$app_dir/postgres-ca.crt"
  "$postgres_dir/postgres-ca.crt"
  "$postgres_dir/postgres-server.crt"
  "$postgres_dir/postgres-server.key"
  "$postgres_dir/pg_hba.conf"
  "$pki_issuer/ca.crt"
  "$pki_issuer/ca.key"
)
tls_present=0
for path in "${tls_expected[@]}"; do
  [[ -f "$path" ]] && tls_present=$((tls_present + 1))
done
if [[ "$tls_present" -ne 0 && "$tls_present" -ne "${#tls_expected[@]}" ]]; then
  printf 'staging PostgreSQL TLS material is partial; refusing implicit repair or rotation\n' >&2
  exit 1
fi

if [[ "$tls_present" -eq 0 ]]; then
  install -d -m 0700 -o root -g root "$pki_root" "$pki_issuer"
  pki_tmp="$(mktemp -d /tmp/asteria-staging-postgres-pki.XXXXXXXX)"
  [[ "$pki_tmp" =~ ^/tmp/asteria-staging-postgres-pki\.[A-Za-z0-9]{8}$ ]] || exit 1
  cleanup_pki_tmp() {
    rm -rf -- "$pki_tmp"
  }
  trap cleanup_pki_tmp EXIT
  umask 077
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$pki_tmp/ca.key"
  openssl req -x509 -new -sha256 -days 3650 \
    -key "$pki_tmp/ca.key" -out "$pki_tmp/ca.crt" \
    -subj '/CN=Asteria staging PostgreSQL CA' \
    -addext 'basicConstraints=critical,CA:TRUE' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign'
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$pki_tmp/server.key"
  openssl req -new -sha256 -key "$pki_tmp/server.key" -out "$pki_tmp/server.csr" \
    -subj '/CN=postgres'
  cat >"$pki_tmp/server.ext" <<'EOF'
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:postgres
EOF
  openssl x509 -req -sha256 -days 397 \
    -in "$pki_tmp/server.csr" -CA "$pki_tmp/ca.crt" -CAkey "$pki_tmp/ca.key" \
    -CAcreateserial -extfile "$pki_tmp/server.ext" -out "$pki_tmp/server.crt"
  cat >"$pki_tmp/pg_hba.conf" <<'EOF'
local all all trust
hostnossl all all 0.0.0.0/0 reject
hostnossl all all ::/0 reject
hostssl all all 0.0.0.0/0 scram-sha-256
hostssl all all ::/0 scram-sha-256
EOF
  openssl verify -CAfile "$pki_tmp/ca.crt" -verify_hostname postgres "$pki_tmp/server.crt" >/dev/null
  openssl x509 -in "$pki_tmp/ca.crt" -noout -ext basicConstraints | grep -Fq 'CA:TRUE'
  openssl x509 -in "$pki_tmp/ca.crt" -noout -ext keyUsage | grep -Fq 'Certificate Sign, CRL Sign'
  openssl x509 -in "$pki_tmp/server.crt" -checkend $((30 * 24 * 60 * 60)) -noout
  ca_public_key="$(openssl pkey -in "$pki_tmp/ca.key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  ca_certificate_key="$(openssl x509 -in "$pki_tmp/ca.crt" -pubkey -noout | \
    openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  server_public_key="$(openssl pkey -in "$pki_tmp/server.key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  server_certificate_key="$(openssl x509 -in "$pki_tmp/server.crt" -pubkey -noout | \
    openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
  [[ "$ca_public_key" == "$ca_certificate_key" && "$server_public_key" == "$server_certificate_key" ]]

  postgres_password="$(<"$postgres_dir/postgres-password")"
  [[ "$postgres_password" =~ ^[0-9a-f]{48}$ ]] || { printf 'staging PostgreSQL password format is invalid\n' >&2; exit 1; }
  printf 'postgres://asteria:%s@postgres:5432/asteria?sslmode=verify-full&sslrootcert=/var/run/secrets/asteria/postgres-ca.crt&application_name=asteria-drive-staging-api\n' \
    "$postgres_password" >"$pki_tmp/database-url-tls"
  unset postgres_password

  install -m 0400 -o root -g root "$pki_tmp/ca.key" "$pki_issuer/ca.key"
  install -m 0444 -o root -g root "$pki_tmp/ca.crt" "$pki_issuer/ca.crt"
  install -m 0400 -o 65532 -g 65532 "$pki_tmp/ca.crt" "$app_dir/postgres-ca.crt"
  install -m 0400 -o 65532 -g 65532 "$pki_tmp/database-url-tls" "$app_dir/database-url-tls"
  install -m 0640 -o root -g 70 "$pki_tmp/ca.crt" "$postgres_dir/postgres-ca.crt"
  install -m 0640 -o root -g 70 "$pki_tmp/server.crt" "$postgres_dir/postgres-server.crt"
  install -m 0640 -o root -g 70 "$pki_tmp/server.key" "$postgres_dir/postgres-server.key"
  install -m 0640 -o root -g 70 "$pki_tmp/pg_hba.conf" "$postgres_dir/pg_hba.conf"
  trap - EXIT
  cleanup_pki_tmp
fi

require_metadata "$app_dir/database-url-tls" "65532:65532" "400"
require_metadata "$app_dir/postgres-ca.crt" "65532:65532" "400"
require_metadata "$postgres_dir/postgres-ca.crt" "0:70" "640"
require_metadata "$postgres_dir/postgres-server.crt" "0:70" "640"
require_metadata "$postgres_dir/postgres-server.key" "0:70" "640"
require_metadata "$postgres_dir/pg_hba.conf" "0:70" "640"
require_metadata "$pki_issuer/ca.crt" "0:0" "444"
require_metadata "$pki_issuer/ca.key" "0:0" "400"
for directory in "$pki_root" "$pki_issuer"; do
  [[ "$(stat -c '%u:%g:%a' "$directory")" == "0:0:700" ]] || {
    printf 'unsafe staging PostgreSQL PKI directory metadata\n' >&2
    exit 1
  }
done
cmp --silent "$app_dir/postgres-ca.crt" "$postgres_dir/postgres-ca.crt"
cmp --silent "$app_dir/postgres-ca.crt" "$pki_issuer/ca.crt"
openssl x509 -in "$pki_issuer/ca.crt" -noout -ext basicConstraints | grep -Fq 'CA:TRUE'
openssl x509 -in "$pki_issuer/ca.crt" -noout -ext keyUsage | grep -Fq 'Certificate Sign, CRL Sign'
openssl verify -CAfile "$app_dir/postgres-ca.crt" -verify_hostname postgres \
  "$postgres_dir/postgres-server.crt" >/dev/null
openssl x509 -in "$postgres_dir/postgres-server.crt" -checkend $((30 * 24 * 60 * 60)) -noout
leaf_san="$(openssl x509 -in "$postgres_dir/postgres-server.crt" -noout -ext subjectAltName |
  awk 'NR > 1 { gsub(/[[:space:]]/, ""); printf "%s", $0 }')"
[[ "$leaf_san" == "DNS:postgres" ]]
ca_public_key="$(openssl pkey -in "$pki_issuer/ca.key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
ca_certificate_key="$(openssl x509 -in "$pki_issuer/ca.crt" -pubkey -noout |
  openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
server_public_key="$(openssl pkey -in "$postgres_dir/postgres-server.key" -pubout -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
server_certificate_key="$(openssl x509 -in "$postgres_dir/postgres-server.crt" -pubkey -noout |
  openssl pkey -pubin -outform DER 2>/dev/null | sha256sum | awk '{print $1}')"
[[ "$ca_public_key" == "$ca_certificate_key" && "$server_public_key" == "$server_certificate_key" ]]
[[ "$(grep -Ec '^(local|hostnossl|hostssl) ' "$postgres_dir/pg_hba.conf")" -eq 5 ]]
grep -Fxq 'local all all trust' "$postgres_dir/pg_hba.conf"
grep -Fxq 'hostnossl all all 0.0.0.0/0 reject' "$postgres_dir/pg_hba.conf"
grep -Fxq 'hostnossl all all ::/0 reject' "$postgres_dir/pg_hba.conf"
grep -Fxq 'hostssl all all 0.0.0.0/0 scram-sha-256' "$postgres_dir/pg_hba.conf"
grep -Fxq 'hostssl all all ::/0 scram-sha-256' "$postgres_dir/pg_hba.conf"

python3 - "$app_dir/database-url-tls" "$postgres_dir/postgres-password" <<'PY'
import sys
import urllib.parse

with open(sys.argv[1], encoding="utf-8") as handle:
    database = urllib.parse.urlsplit(handle.read().strip())
with open(sys.argv[2], encoding="utf-8") as handle:
    password = handle.read().strip()
query = urllib.parse.parse_qs(database.query, keep_blank_values=True)
if database.scheme not in {"postgres", "postgresql"} or database.hostname != "postgres":
    raise SystemExit("staging TLS database URL is invalid")
if database.username != "asteria" or urllib.parse.unquote(database.password or "") != password:
    raise SystemExit("staging TLS database credentials are inconsistent")
expected = {
    "sslmode": ["verify-full"],
    "sslrootcert": ["/var/run/secrets/asteria/postgres-ca.crt"],
    "application_name": ["asteria-drive-staging-api"],
}
if query != expected:
    raise SystemExit("staging TLS database parameters are invalid")
PY

dispatcher_tmp="$(mktemp /tmp/asteria-staging-dispatch.XXXXXXXX)"
config_tmp="$(mktemp /tmp/asteria-staging-dispatch-config.XXXXXXXX)"
monitor_tmp="$(mktemp /tmp/asteria-staging-monitor.XXXXXXXX)"
recovery_tmp="$(mktemp /tmp/asteria-staging-recovery.XXXXXXXX)"
cleanup_bootstrap() {
  rm -f -- "$dispatcher_tmp" "$config_tmp" "$monitor_tmp" "$recovery_tmp"
}
trap cleanup_bootstrap EXIT

printf '%s' "$monitor_source_b64" | base64 -d >"$monitor_tmp"
[[ "$(sha256sum "$monitor_tmp" | awk '{print $1}')" == "$expected_monitor_script_sha256" ]] || {
  printf 'staging monitor script digest mismatch\n' >&2
  exit 1
}
bash -n "$monitor_tmp"

printf '%s' "$recovery_source_b64" | base64 -d >"$recovery_tmp"
[[ "$(sha256sum "$recovery_tmp" | awk '{print $1}')" == "$expected_recovery_script_sha256" ]] || {
  printf 'staging recovery script digest mismatch\n' >&2
  exit 1
}
bash -n "$recovery_tmp"

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
  status)
    [[ "${#fields[@]}" -eq 5 ]] || fail
    validate_run
    workflow_sha="${fields[3]}"
    requested_monitor_sha="${fields[4]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ "$requested_monitor_sha" =~ ^[0-9a-f]{64}$ ]] || fail
    [[ "$requested_monitor_sha" == "$monitor_script_sha256" ]] || fail
    [[ -f "$monitor_path" && ! -L "$monitor_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$monitor_path")" == "0:0:755" ]] || fail
    [[ "$(sha256sum "$monitor_path" | awk '{print $1}')" == "$monitor_script_sha256" ]] || fail
    exec env ASTERIA_DEPLOY_ROOT="$deploy_root" \
      "$monitor_path" "$run_id" "$run_attempt" "$workflow_sha" "$requested_monitor_sha"
    ;;
  recovery)
    [[ "${#fields[@]}" -eq 5 ]] || fail
    validate_run
    workflow_sha="${fields[3]}"
    requested_recovery_sha="${fields[4]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ "$requested_recovery_sha" =~ ^[0-9a-f]{64}$ ]] || fail
    [[ "$requested_recovery_sha" == "$recovery_script_sha256" ]] || fail
    [[ -f "$recovery_path" && ! -L "$recovery_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$recovery_path")" == "0:0:755" ]] || fail
    [[ "$(sha256sum "$recovery_path" | awk '{print $1}')" == "$recovery_script_sha256" ]] || fail
    exec env ASTERIA_DEPLOY_ROOT="$deploy_root" \
      "$recovery_path" "$run_id" "$run_attempt" "$workflow_sha"
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
  printf 'monitor_script_sha256=%q\n' "$expected_monitor_script_sha256"
  printf 'recovery_script_sha256=%q\n' "$expected_recovery_script_sha256"
  printf 'deploy_root=%q\n' "$deploy_root"
  printf 'monitor_path=%q\n' "$monitor_path"
  printf 'recovery_path=%q\n' "$recovery_path"
} >"$config_tmp"

install -d -m 0755 -o root -g root /usr/local/libexec
install -d -m 0755 -o root -g root /etc/asteria-drive
for directory in /usr/local/libexec /etc/asteria-drive; do
  [[ "$(stat -c '%u:%g:%a' "$directory")" == "0:0:755" ]] || {
    printf 'unsafe staging dispatcher directory metadata: %s\n' "$directory" >&2
    exit 1
  }
done
install -m 0644 -o root -g root "$config_tmp" "$dispatcher_config"
install -m 0755 -o root -g root "$monitor_tmp" "$monitor_path"
install -m 0755 -o root -g root "$recovery_tmp" "$recovery_path"
install -m 0755 -o root -g root "$dispatcher_tmp" "$dispatcher_path"
install -d -m 0700 -o "$deploy_user" -g "$deploy_group" "$deploy_home/.ssh"
printf 'restrict,command="%s" %s\n' "$dispatcher_path" "$public_key" >"$deploy_home/.ssh/authorized_keys"
chown "$deploy_user:$deploy_group" "$deploy_home/.ssh/authorized_keys"
chmod 0600 "$deploy_home/.ssh/authorized_keys"

trap - EXIT
cleanup_bootstrap
printf 'staging host bootstrap is ready\n'
