#!/usr/bin/env bash
# Create/update rf2vc HTTP basic-auth secret + GHCR pull secret.
# vCenter credentials are managed in the admin UI (PVC state.json).
# Optional: export GOVC_* to seed the first vCenter on empty store.
set -euo pipefail

NS="${RF2VC_NAMESPACE:-rf2vc-system}"
CONTEXT="${OC_CONTEXT:-}"
OC=(oc)
if [[ -n "${CONTEXT}" ]]; then
  OC=(oc --context="${CONTEXT}")
fi

AUTH_USER="${RF2VC_AUTH_USERNAME:-redfish}"
AUTH_PASS="${RF2VC_AUTH_PASSWORD:-}"

if [[ -z "${AUTH_PASS}" ]]; then
  echo "Set RF2VC_AUTH_PASSWORD" >&2
  exit 1
fi

"${OC[@]}" create namespace "${NS}" --dry-run=client -o yaml | "${OC[@]}" apply -f -

if "${OC[@]}" get secret dasmlab-ghcr-pull -n ocp-dim-tool-system >/dev/null 2>&1; then
  "${OC[@]}" get secret dasmlab-ghcr-pull -n ocp-dim-tool-system -o yaml \
    | sed -e "s/namespace: ocp-dim-tool-system/namespace: ${NS}/" -e '/resourceVersion:/d' -e '/uid:/d' -e '/creationTimestamp:/d' \
    | "${OC[@]}" apply -f -
fi

"${OC[@]}" create secret generic rf2vc-gateway \
  -n "${NS}" \
  --from-literal=auth-username="${AUTH_USER}" \
  --from-literal=auth-password="${AUTH_PASS}" \
  --dry-run=client -o yaml | "${OC[@]}" apply -f -

echo "Auth secret ready in ${NS}"
echo "Add vCenters + UUID map in the UI: https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/"
if [[ -n "${GOVC_URL:-}" ]]; then
  echo "Note: GOVC_* is set — pod will seed a vCenter on empty PVC if those vars are also in the Deployment env."
fi
