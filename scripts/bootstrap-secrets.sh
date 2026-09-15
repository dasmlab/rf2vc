#!/usr/bin/env bash
# Create/update rf2vc HTTP basic-auth secret.
# Image is public on ghcr.io/dasmlab/rf2vc — no pull secret required.
# vCenter credentials are managed in the admin UI (PVC state.json).
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

"${OC[@]}" create secret generic rf2vc-gateway \
  -n "${NS}" \
  --from-literal=auth-username="${AUTH_USER}" \
  --from-literal=auth-password="${AUTH_PASS}" \
  --dry-run=client -o yaml | "${OC[@]}" apply -f -

echo "Auth secret ready in ${NS}"
echo "Next: oc apply -k deploy/openshift/  (edit route host / PVC storageClass as needed)"
