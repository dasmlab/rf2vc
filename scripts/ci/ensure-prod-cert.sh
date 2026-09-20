#!/usr/bin/env bash
# Ensure HAProxy CERT for production FQDN on 2026-prod-1.
# Uses remotessh (Go) — no system ssh required on the runner.
set -euo pipefail

FQDN="${1:?usage: ensure-prod-cert.sh <fqdn>}"
PROXY_HOST="${PREVIEW_PROXY_HOST:-10.20.1.10}"
PROXY_USER="${PREVIEW_PROXY_USER:-dasm}"
PROXY_DIR="${PREVIEW_PROXY_DIR:-/home/dasm/dasmlab-internal/new_haproxy}"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
REMOTE_SSH=(go run "${ROOT}/scripts/ci/remotessh")

cleanup_key() {
  if [[ -n "${_SSH_KEY_TMP:-}" && -f "${_SSH_KEY_TMP}" ]]; then
    rm -f "${_SSH_KEY_TMP}"
  fi
}
trap cleanup_key EXIT

if [[ -n "${SSH_IDENTITY_FILE:-}" && -f "${SSH_IDENTITY_FILE}" ]]; then
  REMOTE_SSH+=(-i "${SSH_IDENTITY_FILE}")
elif [[ -n "${PREVIEW_PROXY_SSH_KEY:-}" ]]; then
  _SSH_KEY_TMP="$(mktemp)"
  printf '%s\n' "${PREVIEW_PROXY_SSH_KEY}" | tr -d '\r' > "${_SSH_KEY_TMP}"
  chmod 600 "${_SSH_KEY_TMP}"
  REMOTE_SSH+=(-i "${_SSH_KEY_TMP}")
else
  # Self-hosted runners often already have the proxy key on disk.
  for f in /home/dasm/.ssh/id_rsa "$HOME/.ssh/id_rsa" /home/dasm/.ssh/id_ecdsa "$HOME/.ssh/id_ecdsa"; do
    if [[ -f "$f" ]]; then
      SSH_IDENTITY_FILE="$f"
      break
    fi
  done
  if [[ -n "${SSH_IDENTITY_FILE:-}" && -f "${SSH_IDENTITY_FILE}" ]]; then
    echo "Using runner SSH identity ${SSH_IDENTITY_FILE}"
    REMOTE_SSH+=(-i "${SSH_IDENTITY_FILE}")
  else
    echo "ERROR: set PREVIEW_PROXY_SSH_KEY or SSH_IDENTITY_FILE (or place id_rsa on the runner)" >&2
    exit 1
  fi
fi

"${REMOTE_SSH[@]}" "${PROXY_USER}@${PROXY_HOST}" bash -s -- "${FQDN}" "${PROXY_DIR}" <<'EOS'
set -euo pipefail
FQDN="$1"
DIR="$2"
cd "$DIR"
if grep -Fq "=${FQDN}" runme.sh; then
  echo "HAProxy CERT already present for ${FQDN}"
  exit 0
fi

last="$(grep -oE 'CERT[0-9]+=' runme.sh | grep -oE '[0-9]+' | sort -n | tail -1)"
next=$((last + 1))
echo "Adding CERT${next}=${FQDN} to runme.sh and recreating new-haproxy"

tmp="$(mktemp)"
awk -v n="$next" -v h="$FQDN" '
  / -e EMAIL=/ && !done {
    print "    -e CERT" n "=" h " \\"
    done=1
  }
  { print }
' runme.sh > "$tmp"
mv "$tmp" runme.sh
chmod +x runme.sh

./runme.sh
echo "HAProxy updated for ${FQDN} (CERT${next})"
EOS
