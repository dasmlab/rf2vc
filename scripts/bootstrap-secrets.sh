#!/usr/bin/env bash
# Create/update rf2vc secrets + GHCR pull secret on the target cluster.
# Does not print secret values.
set -euo pipefail

NS="${RF2VC_NAMESPACE:-rf2vc-system}"
CONTEXT="${OC_CONTEXT:-}"
OC=(oc)
if [[ -n "${CONTEXT}" ]]; then
  OC=(oc --context="${CONTEXT}")
fi

AUTH_USER="${RF2VC_AUTH_USERNAME:-redfish}"
AUTH_PASS="${RF2VC_AUTH_PASSWORD:-}"
VC_URL="${RF2VC_VSPHERE_URL:-https://vcenter.example.local}"
VC_USER="${RF2VC_VSPHERE_USERNAME:-}"
VC_PASS="${RF2VC_VSPHERE_PASSWORD:-}"
VC_DC="${RF2VC_VSPHERE_DATACENTER:-Datacenter}"
VC_DS="${RF2VC_VSPHERE_DATASTORE:-datastore1}"

if [[ -z "${AUTH_PASS}" || -z "${VC_USER}" || -z "${VC_PASS}" ]]; then
  echo "Set RF2VC_AUTH_PASSWORD, RF2VC_VSPHERE_USERNAME, RF2VC_VSPHERE_PASSWORD" >&2
  echo "(optional: RF2VC_VSPHERE_URL / DATACENTER / DATASTORE / AUTH_USERNAME)" >&2
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
  --from-literal=vsphere-url="${VC_URL}" \
  --from-literal=vsphere-username="${VC_USER}" \
  --from-literal=vsphere-password="${VC_PASS}" \
  --from-literal=vsphere-datacenter="${VC_DC}" \
  --from-literal=vsphere-datastore="${VC_DS}" \
  --dry-run=client -o yaml | "${OC[@]}" apply -f -

echo "Secrets ready in ${NS}"
