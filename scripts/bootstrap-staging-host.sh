#!/usr/bin/env bash
set -Eeuo pipefail

deploy_user="${ASTERIA_DEPLOY_USER:-asteria-deploy}"
deploy_root="${ASTERIA_DEPLOY_ROOT:-/opt/asteria-drive/staging}"
lock_dir="/run/asteria-drive"
lock_path="${lock_dir}/staging.lock"
public_key_b64="${ASTERIA_DEPLOY_PUBLIC_KEY_B64:-}"
monitor_source_b64="${ASTERIA_STAGING_MONITOR_B64:-}"
monitor_v011_source_b64="${ASTERIA_STAGING_MONITOR_V011_B64:-}"
recovery_source_b64="${ASTERIA_STAGING_RECOVERY_B64:-}"
recovery_v011_source_b64="${ASTERIA_STAGING_RECOVERY_V011_B64:-}"
controller_source_b64="${ASTERIA_STAGING_CONTROLLER_B64:-}"
verifier_source_b64="${ASTERIA_STAGING_RUNTIME_VERIFIER_B64:-}"
dispatcher_path="/usr/local/libexec/asteria-staging-dispatch"
dispatcher_config="/etc/asteria-drive/staging-dispatch.conf"
controller_path="/usr/local/libexec/asteria-staging-controller"
verifier_path="/usr/local/libexec/staging_runtime_verifier.py"
releases_path="/etc/asteria-drive/staging-releases.json"
monitor_path="/usr/local/libexec/asteria-staging-monitor"
recovery_path="/usr/local/libexec/asteria-staging-recovery"
monitor_v011_path="/usr/local/libexec/asteria-staging-monitor-v0.1.1"
recovery_v011_path="/usr/local/libexec/asteria-staging-recovery-v0.1.1"
expected_compose_sha256="cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005"
expected_deploy_script_sha256="007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f"
legacy_compose_sha256="aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637"
legacy_deploy_script_sha256="cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044"
expected_monitor_script_sha256="d1e186cb375bf22fb910118e122d6003a6904c4345134d503e212aa051916d69"
expected_monitor_v011_script_sha256="3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279"
expected_recovery_script_sha256="91c45a195b773b98e0dc70f3843f8f4af8efc05e2c6f66c7811cccabc633c1b4"
expected_recovery_v011_script_sha256="3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a"
expected_controller_sha256="5ccb8d18aae92d1040c20d76eb27dadd0c9ca7c04334d733b34bc4f69d9eaa38"
expected_verifier_sha256="d4da4f696a5e2e1a93f0e0c4c51ecf6c09684842f665d9e620705494d837ffdc"

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
if [[ -z "$monitor_v011_source_b64" || ! "$monitor_v011_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#monitor_v011_source_b64}" -gt 1048576 ]]; then
  printf 'ASTERIA_STAGING_MONITOR_V011_B64 is invalid\n' >&2
  exit 1
fi
if [[ -z "$recovery_v011_source_b64" || ! "$recovery_v011_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#recovery_v011_source_b64}" -gt 2097152 ]]; then
  printf 'ASTERIA_STAGING_RECOVERY_V011_B64 is invalid\n' >&2
  exit 1
fi
if [[ -z "$controller_source_b64" || ! "$controller_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#controller_source_b64}" -gt 1048576 ]]; then
  printf 'ASTERIA_STAGING_CONTROLLER_B64 is invalid\n' >&2
  exit 1
fi
if [[ -z "$verifier_source_b64" || ! "$verifier_source_b64" =~ ^[A-Za-z0-9+/=]+$ || "${#verifier_source_b64}" -gt 1048576 ]]; then
  printf 'ASTERIA_STAGING_RUNTIME_VERIFIER_B64 is invalid\n' >&2
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
for command in awk base64 cmp flock grep openssl python3 sha256sum; do
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
deploy_gid="$(id -g "$deploy_group")"
install -d -m 0750 -o "$deploy_user" -g "$deploy_group" "$deploy_root"
install -d -m 0750 -o root -g "$deploy_group" "$lock_dir"
[[ "$(stat -c '%u:%g:%a' "$lock_dir")" == "0:${deploy_gid}:750" ]] || {
  printf 'unsafe staging lock directory ownership or mode\n' >&2
  exit 1
}
if [[ -L "$lock_path" || ( -e "$lock_path" && ! -f "$lock_path" ) ]]; then
  printf 'unsafe staging lock path\n' >&2
  exit 1
fi
if [[ -e "$lock_path" ]]; then
  [[ "$(stat -c '%u:%g:%a' "$lock_path")" == "0:${deploy_gid}:660" ]] || {
    printf 'unsafe staging lock ownership or mode\n' >&2
    exit 1
  }
else
  install -m 0660 -o root -g "$deploy_group" /dev/null "$lock_path"
fi
install -d -m 0755 -o root -g root /etc/tmpfiles.d
printf 'd %s 0750 root %s - -\nf %s 0660 root %s - -\n' \
  "$lock_dir" "$deploy_group" "$lock_path" "$deploy_group" >/etc/tmpfiles.d/asteria-drive-staging.conf
chmod 0644 /etc/tmpfiles.d/asteria-drive-staging.conf
chown root:root /etc/tmpfiles.d/asteria-drive-staging.conf

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
monitor_v011_tmp="$(mktemp /tmp/asteria-staging-monitor-v011.XXXXXXXX)"
recovery_tmp="$(mktemp /tmp/asteria-staging-recovery.XXXXXXXX)"
recovery_v011_tmp="$(mktemp /tmp/asteria-staging-recovery-v011.XXXXXXXX)"
controller_tmp="$(mktemp /tmp/asteria-staging-controller.XXXXXXXX)"
verifier_tmp="$(mktemp /tmp/asteria-staging-runtime-verifier.XXXXXXXX)"
releases_tmp="$(mktemp /tmp/asteria-staging-releases.XXXXXXXX)"
cleanup_bootstrap() {
  rm -f -- "$dispatcher_tmp" "$config_tmp" "$monitor_tmp" "$monitor_v011_tmp" "$recovery_tmp" "$recovery_v011_tmp" "$controller_tmp" "$verifier_tmp" "$releases_tmp"
}
trap cleanup_bootstrap EXIT

printf '%s' "$monitor_source_b64" | base64 -d >"$monitor_tmp"
[[ "$(sha256sum "$monitor_tmp" | awk '{print $1}')" == "$expected_monitor_script_sha256" ]] || {
  printf 'staging monitor script digest mismatch\n' >&2
  exit 1
}
bash -n "$monitor_tmp"

printf '%s' "$monitor_v011_source_b64" | base64 -d >"$monitor_v011_tmp"
[[ "$(sha256sum "$monitor_v011_tmp" | awk '{print $1}')" == "$expected_monitor_v011_script_sha256" ]] || {
  printf 'staging v0.1.1 monitor script digest mismatch\n' >&2
  exit 1
}
bash -n "$monitor_v011_tmp"

printf '%s' "$recovery_source_b64" | base64 -d >"$recovery_tmp"
[[ "$(sha256sum "$recovery_tmp" | awk '{print $1}')" == "$expected_recovery_script_sha256" ]] || {
  printf 'staging recovery script digest mismatch\n' >&2
  exit 1
}
bash -n "$recovery_tmp"

printf '%s' "$recovery_v011_source_b64" | base64 -d >"$recovery_v011_tmp"
[[ "$(sha256sum "$recovery_v011_tmp" | awk '{print $1}')" == "$expected_recovery_v011_script_sha256" ]] || {
  printf 'staging v0.1.1 recovery script digest mismatch\n' >&2
  exit 1
}
bash -n "$recovery_v011_tmp"

printf '%s' "$controller_source_b64" | base64 -d >"$controller_tmp"
[[ "$(sha256sum "$controller_tmp" | awk '{print $1}')" == "$expected_controller_sha256" ]] || {
  printf 'staging controller script digest mismatch\n' >&2
  exit 1
}
python3 -m py_compile "$controller_tmp"

printf '%s' "$verifier_source_b64" | base64 -d >"$verifier_tmp"
[[ "$(sha256sum "$verifier_tmp" | awk '{print $1}')" == "$expected_verifier_sha256" ]] || {
  printf 'staging runtime verifier digest mismatch\n' >&2
  exit 1
}
python3 -m py_compile "$verifier_tmp"

cat >"$dispatcher_tmp" <<'DISPATCHER'
#!/usr/bin/env bash
set -Eeuo pipefail

source /etc/asteria-drive/staging-dispatch.conf

fail() {
  printf 'staging deploy command rejected\n' >&2
  exit 1
}

validate_lock() {
  [[ -f "$lock_path" && ! -L "$lock_path" ]] || fail
  [[ "$(stat -c '%u:%g:%a' "$lock_path")" == "0:${deploy_gid}:660" ]] || fail
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

receive_release_file() {
  local destination="$1" limit="$2" temporary size actual_sha
  shift 2
  temporary="${destination}.tmp"
  rm -f -- "$temporary"
  umask 077
  head -c "$((limit + 1))" >"$temporary"
  size="$(stat -c '%s' "$temporary")"
  [[ "$size" -gt 0 && "$size" -le "$limit" ]] || fail
  actual_sha="$(sha256sum "$temporary" | awk '{print $1}')"
  local allowed
  for allowed in "$@"; do
    if [[ "$actual_sha" == "$allowed" ]]; then
      mv -f -- "$temporary" "$destination"
      chmod 0600 "$destination"
      return 0
    fi
  done
  rm -f -- "$temporary"
  fail
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
    receive_release_file "$run_dir/compose.yaml" 262144 "$compose_sha256" "$legacy_compose_sha256"
    ;;
  upload-script)
    [[ "${#fields[@]}" -eq 3 ]] || fail
    validate_run
    require_run_dir
    receive_release_file "$run_dir/deploy-staging.sh" 524288 "$deploy_script_sha256" "$legacy_deploy_script_sha256"
    ;;
  deploy)
    [[ "${#fields[@]}" -eq 4 ]] || fail
    validate_run
    require_run_dir
    workflow_sha="${fields[3]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ -f "$run_dir/compose.yaml" && ! -L "$run_dir/compose.yaml" ]] || fail
    [[ -f "$run_dir/deploy-staging.sh" && ! -L "$run_dir/deploy-staging.sh" ]] || fail
    compose_actual="$(sha256sum "$run_dir/compose.yaml" | awk '{print $1}')"
    script_actual="$(sha256sum "$run_dir/deploy-staging.sh" | awk '{print $1}')"
    if [[ "$compose_actual" == "$compose_sha256" && "$script_actual" == "$deploy_script_sha256" ]]; then
      target_version="v0.2.0"
    elif [[ "$compose_actual" == "$legacy_compose_sha256" && "$script_actual" == "$legacy_deploy_script_sha256" ]]; then
      target_version="v0.1.1"
    else
      fail
    fi
    [[ -f "$controller_path" && ! -L "$controller_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$controller_path")" == "0:0:755" ]] || fail
    [[ "$(sha256sum "$controller_path" | awk '{print $1}')" == "$controller_sha256" ]] || fail
    [[ -f "$verifier_path" && ! -L "$verifier_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$verifier_path")" == "0:0:644" ]] || fail
    [[ "$(sha256sum "$verifier_path" | awk '{print $1}')" == "$verifier_sha256" ]] || fail
    validate_lock
    exec flock -n "$lock_path" env ASTERIA_DEPLOY_ROOT="$deploy_root" \
      "$controller_path" deploy "$run_id" "$run_attempt" "$workflow_sha" "$target_version" "$run_dir"
    ;;
  status)
    [[ "${#fields[@]}" -eq 5 ]] || fail
    validate_run
    workflow_sha="${fields[3]}"
    requested_monitor_sha="${fields[4]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ "$requested_monitor_sha" =~ ^[0-9a-f]{64}$ ]] || fail
    case "$requested_monitor_sha" in
      "$monitor_script_sha256") selected_monitor_path="$monitor_path" ;;
      "$monitor_v011_script_sha256") selected_monitor_path="$monitor_v011_path" ;;
      *) fail ;;
    esac
    [[ -f "$selected_monitor_path" && ! -L "$selected_monitor_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$selected_monitor_path")" == "0:0:755" ]] || fail
    [[ "$(sha256sum "$selected_monitor_path" | awk '{print $1}')" == "$requested_monitor_sha" ]] || fail
    validate_lock
    exec flock -n "$lock_path" env ASTERIA_DEPLOY_ROOT="$deploy_root" \
      "$selected_monitor_path" "$run_id" "$run_attempt" "$workflow_sha" "$requested_monitor_sha"
    ;;
  recovery)
    [[ "${#fields[@]}" -eq 5 ]] || fail
    validate_run
    workflow_sha="${fields[3]}"
    requested_recovery_sha="${fields[4]}"
    [[ "$workflow_sha" =~ ^[0-9a-f]{40}$ ]] || fail
    [[ "$requested_recovery_sha" =~ ^[0-9a-f]{64}$ ]] || fail
    case "$requested_recovery_sha" in
      "$recovery_script_sha256") selected_recovery_path="$recovery_path" ;;
      "$recovery_v011_script_sha256") selected_recovery_path="$recovery_v011_path" ;;
      *) fail ;;
    esac
    [[ -f "$selected_recovery_path" && ! -L "$selected_recovery_path" ]] || fail
    [[ "$(stat -c '%u:%g:%a' "$selected_recovery_path")" == "0:0:755" ]] || fail
    [[ "$(sha256sum "$selected_recovery_path" | awk '{print $1}')" == "$requested_recovery_sha" ]] || fail
    validate_lock
    exec flock -n "$lock_path" env ASTERIA_DEPLOY_ROOT="$deploy_root" \
      "$selected_recovery_path" "$run_id" "$run_attempt" "$workflow_sha"
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
  printf 'legacy_compose_sha256=%q\n' "$legacy_compose_sha256"
  printf 'legacy_deploy_script_sha256=%q\n' "$legacy_deploy_script_sha256"
  printf 'monitor_script_sha256=%q\n' "$expected_monitor_script_sha256"
  printf 'monitor_v011_script_sha256=%q\n' "$expected_monitor_v011_script_sha256"
  printf 'recovery_script_sha256=%q\n' "$expected_recovery_script_sha256"
  printf 'recovery_v011_script_sha256=%q\n' "$expected_recovery_v011_script_sha256"
  printf 'deploy_root=%q\n' "$deploy_root"
  printf 'lock_path=%q\n' "$lock_path"
  printf 'deploy_gid=%q\n' "$deploy_gid"
  printf 'monitor_path=%q\n' "$monitor_path"
  printf 'monitor_v011_path=%q\n' "$monitor_v011_path"
  printf 'recovery_path=%q\n' "$recovery_path"
  printf 'recovery_v011_path=%q\n' "$recovery_v011_path"
  printf 'controller_path=%q\n' "$controller_path"
  printf 'controller_sha256=%q\n' "$expected_controller_sha256"
  printf 'verifier_path=%q\n' "$verifier_path"
  printf 'verifier_sha256=%q\n' "$expected_verifier_sha256"
  printf 'releases_path=%q\n' "$releases_path"
} >"$config_tmp"

printf '%s\n' \
  '{"v0.1.1":{"compose_sha256":"aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637","deploy_script_sha256":"cd200fa32fdc2314817308479c99fe6425a481ff3bcd7384a60e145b2ad45044","schema_version":3,"source_commit":"e8d26ded6e9138bbfdeac60f2487c5f835ab61a5","image":"ghcr.io/baicie/asteria-drive@sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842"},"v0.2.0":{"compose_sha256":"cba2ba284a8f17231976ca40b99e77c4233f7948992372d703203cd98bdd3005","deploy_script_sha256":"007eb3a34bc5eff5bdff1db247ef9ed2b396814eac8231a0df71579a3f79138f","schema_version":3,"source_commit":"5169da638beac679c9cfed66757159474de879dc","image":"ghcr.io/baicie/asteria-drive@sha256:964e71a496e0800a48aaeb9d513f3927d175927a7e271dde37e0570532dc4853"}}' \
  >"$releases_tmp"

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
install -m 0755 -o root -g root "$monitor_v011_tmp" "$monitor_v011_path"
install -m 0755 -o root -g root "$recovery_tmp" "$recovery_path"
install -m 0755 -o root -g root "$recovery_v011_tmp" "$recovery_v011_path"
install -m 0755 -o root -g root "$controller_tmp" "$controller_path"
install -m 0644 -o root -g root "$verifier_tmp" "$verifier_path"
install -m 0644 -o root -g root "$releases_tmp" "$releases_path"
install -m 0755 -o root -g root "$dispatcher_tmp" "$dispatcher_path"
install -d -m 0700 -o "$deploy_user" -g "$deploy_group" "$deploy_home/.ssh"
printf 'restrict,command="%s" %s\n' "$dispatcher_path" "$public_key" >"$deploy_home/.ssh/authorized_keys"
chown "$deploy_user:$deploy_group" "$deploy_home/.ssh/authorized_keys"
chmod 0600 "$deploy_home/.ssh/authorized_keys"

trap - EXIT
cleanup_bootstrap
printf 'staging host bootstrap is ready\n'
