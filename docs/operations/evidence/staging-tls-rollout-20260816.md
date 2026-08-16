# Staging TLS Rollout Evidence - 2026-08-16

## Scope

This record closes the executable staging rollout for Asteria Drive `v0.1.1`
and the follow-up PostgreSQL TLS monitor and recovery evidence. Two server
inventory CSV records were parsed without printing credentials. The lower-load
target was selected, its ED25519 host key was fixed to
`SHA256:SmRugAjHQE7eyT6/82Dglw259NDzSVbsYtBZNRAzl/Q`, and the GitHub `staging`
Environment connection parameters were aligned to that target before the final
runs.

No staging data volume or server-managed Secret volume was removed. The existing
deploy private key remained in the GitHub Environment; bootstrap consumed only
its installed public key. Passwords, private keys, DSNs, CA private material, and
raw remote stderr are not retained in this record.

The claim boundary remains `staging-not-production`. This evidence does not prove
public ingress, a platform-managed database CA, production PITR/WAL, object
version recovery, production HA, or an independent production approval.

## Immutable identity

| Item | Value |
| --- | --- |
| Staging TLS feature PR | [#38](https://github.com/baicie/asteria-drive/pull/38), merge `1b3b7773e276f8f871f32d492f1070242685cd93` |
| Secret preflight fix PR | [#39](https://github.com/baicie/asteria-drive/pull/39), merge `75f4a7e6afae6d8183c660f456ff34349aa70b7a` |
| TLS boolean fix PR | [#52](https://github.com/baicie/asteria-drive/pull/52), merge `eef47e3ba9bf64c340941832ec1eefb675dae003` |
| Secret owner probe fix PR | [#53](https://github.com/baicie/asteria-drive/pull/53), merge `7c6565bfb16d63c8a9ef7fad54df7e91276a504b` |
| Release | `v0.1.1`, source `e8d26ded6e9138bbfdeac60f2487c5f835ab61a5` |
| Application OCI digest | `sha256:2b73f8a7a271c0d7d6c7f73e15987b5e29290437146f07a57b57b9aef031d842` |
| Compose SHA-256 | `aa4336dc8914faaccc304266cba715b8212d10722adb4afd7aeea5d40f4c9637` |
| Monitor script SHA-256 | `3ef39670c2a5a90b9aee4bf926c7dd6b40938038d861457b0ed92ba48765c279` |
| Recovery script SHA-256 | `3b2bb836852893548f1f1d64602745c67b02a307f0c8ad5fcf17c9c5a41f459a` |

PR #53 passed `CI / quality`, `CI / race`, `CI / integration`,
`CI / api-contract`, `Security / govulncheck`,
`Security / dependency-review`, `Security / codeql`, and the additional CodeQL
check. No review approval is recorded, so the independent approval gate remains
open.

## Deployment evidence

Deployment workflow
[31713181762](https://github.com/baicie/asteria-drive/actions/runs/31713181762),
attempt `1`, succeeded from protected-main workflow SHA
`eef47e3ba9bf64c340941832ec1eefb675dae003`. Artifact `9186277164`,
`staging-deployment-evidence`, has digest
`sha256:443a1f734809e6d616558d45eb874e689ac444838dbaf285799d61fd20076a49`.
Independent structured parsing recorded these retained file digests:

| File | SHA-256 |
| --- | --- |
| `deployment-evidence.json` | `b39977c89bca913d1dc2d4dcccd51f4c973bebe44964833f206fa71552bedbf2` |
| `storage-verifier.json` | `72f294a15f03d77bdb863feda866e9a3c0bf9cff451a7d93b072b4918c788227` |
| `collection-status.json` | `ae984af3828b2e296a8e8da93c9ad0581427cf8b9f498436efad9b9fa7ac37e4` |

The v2 deployment evidence binds the exact release tag, source commit, OCI
digest, and Compose digest. Migration remained `3 -> 3`; health, readiness,
authenticated read, upload/download equality, metrics, binary identity,
loopback bindings, capacity guard, and storage verifier all succeeded. The
storage verifier checked two objects with no findings. PostgreSQL evidence shows
one application TLS connection using TLS 1.3 and a 256-bit cipher, `DNS:postgres`
certificate validation, `sslmode=verify-full`, the reviewed HBA policy, SCRAM
password storage, and rejection of plaintext TCP. Disk use held at 63%, and no
rollback was attempted.

## Final host bootstrap

After PR #53 merged, the selected host was bootstrapped from the exact
`origin/main` files through an on-disk launcher. The launcher and all three input
scripts were SHA-256 checked before root execution. A second read-only verifier
then proved:

- the installed monitor and recovery files have the reviewed digests and are
  root-owned mode `0755`;
- the dispatcher is root-owned mode `0755`, its configuration is root-owned mode
  `0644`, and it pins the reviewed Compose, deploy, monitor, and recovery digests;
- `authorized_keys` is a single mode `0600` line owned by `asteria-deploy` and
  forces `/usr/local/libexec/asteria-staging-dispatch`; and
- no diagnostic wrapper remained, and all uploaded bootstrap files were removed.

The probes use separate, read-only Secret-volume mounts with the runtime owners
required by the application (`65532:65532`) and PostgreSQL (`0:70`). The reviewed
helper image is bound once as a global `readonly` value. CI requires the summary
capture, canonical one-line parse, digest validation, cross-volume comparison,
and variable cleanup to remain exact continuous blocks, and rejects mutations
that overwrite a summary or digest before comparison.

## Monitor evidence

Protected-main monitor run
[31953366435](https://github.com/baicie/asteria-drive/actions/runs/31953366435),
attempt `1`, succeeded from
`7c6565bfb16d63c8a9ef7fad54df7e91276a504b`. Artifact `9265267564`,
`staging-monitor-evidence`, has digest
`sha256:65cc421a86b5fee858b1141b98cacaaaf9eab13e32f3f0b045e6f3d5166685bb`.
The independently parsed `monitor-evidence.json` SHA-256 is
`61f0b6a1dbe8850b968d9d31bee5cd16d706bf34abc26ee8da671a0f8a9880e9`.

Duplicate keys were rejected and the exact v2 field allowlist was enforced. The
run ID, attempt, workflow SHA, script digest, Compose digest, and OCI digest all
matched. Compose, all three containers, health, readiness, metrics, loopback
bindings, data-volume filesystems, capacity, PostgreSQL TLS/DSN/HBA, plaintext
rejection, and SCRAM checks were all true. The probe observed load1 `0.07`, memory
use `52.06%`, root-disk use `65%`, `14360129536` bytes available, and one TLS 1.3
application connection with 256-bit cipher strength. The remote stderr capture
was present but empty and bound to the SHA-256 of an empty file.

## Recovery evidence

Protected-main recovery run
[31953850132](https://github.com/baicie/asteria-drive/actions/runs/31953850132),
attempt `1`, succeeded from
`7c6565bfb16d63c8a9ef7fad54df7e91276a504b`. Artifact `9265397749`,
`staging-recovery-evidence`, has digest
`sha256:a5944afd9514755a2469e1c26eacb8836ebdec70549c0bcab5278c5c2477f619`.
The independently parsed `recovery-evidence.json` SHA-256 is
`53da31255f3c82a274515ab35af2dadbfcc9a54faf5edbcb85565aeda6855159`.

The exact v2 allowlist and run identity matched. The logical archive was 51,853
bytes and its catalog was verified. The isolated restore kept schema `3 -> 3`,
checked all 15 tables, matched 17 source rows to 17 restored rows, and reported
storage verifier `2/2` with no findings. Recovered health, readiness,
authenticated read, and metrics passed; capacity and cleanup checks passed; and
the source API had a TLS 1.3 PostgreSQL connection. The remote stderr capture was
empty.

The report intentionally records `object_versions_restored=false` and
`pitr_wal_replayed=false`. It therefore proves a bounded staging logical restore,
not production object recovery, PostgreSQL PITR, or an accepted RPO/RTO.

## Remaining production gates

Public production readiness remains open for platform-owned DNS and TLS ingress,
the production OIDC/IdP lifecycle, managed PostgreSQL `verify-full` trust and
encrypted PITR/WAL, object versioning or Object Lock, KMS or secret-controller
rotation, host-level HA, production monitoring/SIEM and external alert routing,
capacity planning, a real external presign upload/download exercise, and
independent security/platform approval.
