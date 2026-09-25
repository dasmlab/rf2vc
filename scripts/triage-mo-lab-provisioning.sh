#!/usr/bin/env bash
# Triage ACM BMH provisioning stuck in preparing (mo-lab + rf2vc + metal3 + assisted).
# Usage:
#   ./scripts/triage-mo-lab-provisioning.sh
#   NS=mo-lab RF2VC_NS=rf2vc-system ./scripts/triage-mo-lab-provisioning.sh
set -euo pipefail

NS="${NS:-mo-lab}"
RF2VC_NS="${RF2VC_NS:-rf2vc-system}"
METAL3_NS="${METAL3_NS:-openshift-machine-api}"
ASSISTED_NS="${ASSISTED_NS:-multicluster-engine}"
INFRAENV="${INFRAENV:-mo-lab}"
RF2VC_SECRET="${RF2VC_SECRET:-rf2vc-creds}"          # BMH credentialsName in mo-lab
RF2VC_HOST="${RF2VC_HOST:-}"                         # optional override; else parsed from first BMH

RED=$'\033[31m'; GREEN=$'\033[32m'; YEL=$'\033[33m'; BOLD=$'\033[1m'; RST=$'\033[0m'
pass() { echo "${GREEN}PASS${RST}  $*"; }
fail() { echo "${RED}FAIL${RST}  $*"; FAILS=$((FAILS + 1)); }
warn() { echo "${YEL}WARN${RST}  $*"; WARNS=$((WARNS + 1)); }
hdr()  { echo; echo "${BOLD}=== $* ===${RST}"; }
FAILS=0
WARNS=0

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "missing tool: $1"; exit 2; }
}
need oc
need jq
need curl

hdr "Context"
echo "NS=$NS  INFRAENV=$INFRAENV  RF2VC_NS=$RF2VC_NS  METAL3_NS=$METAL3_NS"

# -----------------------------------------------------------------------------
hdr "1) BareMetalHosts"
BMH_JSON=$(oc get bmh -n "$NS" -o json)
BMH_COUNT=$(jq '.items|length' <<<"$BMH_JSON")
if [[ "$BMH_COUNT" -eq 0 ]]; then
  fail "no BMHs in $NS"
else
  pass "$BMH_COUNT BMH(s) present"
fi

echo
printf '%-52s %-8s %-12s %-6s %-8s %s\n' NAME ONLINE STATE POWER DEPLOY ERR
jq -r '.items[]|[
  .metadata.name,
  (.spec.online|tostring),
  (.status.provisioning.state // "?"),
  (.status.poweredOn|tostring),
  (.spec.customDeploy.method // "-"),
  (.status.errorMessage // "" | if length>40 then .[0:40]+"..." else . end)
]|@tsv' <<<"$BMH_JSON" | while IFS=$'\t' read -r n o s p d e; do
  printf '%-52s %-8s %-12s %-6s %-8s %s\n' "$n" "$o" "$s" "$p" "$d" "$e"
done

DEPLOY_OK=$(jq '[.items[]|select(.spec.customDeploy.method=="start_assisted_install")]|length' <<<"$BMH_JSON")
DEPLOY_BAD=$((BMH_COUNT - DEPLOY_OK))
[[ "$DEPLOY_BAD" -eq 0 ]] && pass "all BMHs have customDeploy=start_assisted_install" \
  || fail "$DEPLOY_BAD BMH(s) missing customDeploy start_assisted_install (BMAC not done)"

PREP=$(jq '[.items[]|select(.status.provisioning.state=="preparing")]|length' <<<"$BMH_JSON")
[[ "$PREP" -gt 0 ]] && warn "$PREP BMH(s) stuck in preparing"

EMPTY_IMG=$(jq '[.items[]|select((.status.provisioning.image.url // "")=="")]|length' <<<"$BMH_JSON")
[[ "$EMPTY_IMG" -gt 0 ]] && warn "$EMPTY_IMG BMH(s) have empty status.provisioning.image.url (normal early; bad if stuck)"

# infraenv label
NOLABEL=$(jq '[.items[]|select((.metadata.labels["infraenvs.agent-install.openshift.io"] // "")=="")]|length' <<<"$BMH_JSON")
[[ "$NOLABEL" -eq 0 ]] && pass "all BMHs labeled infraenvs.agent-install.openshift.io" \
  || fail "$NOLABEL BMH(s) missing infraenvs.agent-install.openshift.io label"

# -----------------------------------------------------------------------------
hdr "2) PreprovisioningImages"
PPI_JSON=$(oc get preprovisioningimage -n "$NS" -o json 2>/dev/null || echo '{"items":[]}')
PPI_COUNT=$(jq '.items|length' <<<"$PPI_JSON")
[[ "$PPI_COUNT" -eq "$BMH_COUNT" ]] && pass "PPI count matches BMH ($PPI_COUNT)" \
  || warn "PPI count=$PPI_COUNT BMH count=$BMH_COUNT"

# metal3 uses condition type "Ready"
echo
printf '%-52s %-6s %-20s %s\n' NAME READY REASON URL
jq -r '
  .items[] |
  (.status.conditions // []) as $conds |
  ($conds | map(select(.type == "Ready" or .type == "ImageReady")) | .[0] // {}) as $c |
  [
    .metadata.name,
    ($c.status // "?"),
    ($c.reason // "?"),
    ( (.status.imageUrl // "") as $u | if ($u | length) > 60 then $u[0:60] + "..." else $u end )
  ] | @tsv
' <<<"$PPI_JSON" | while IFS=$'\t' read -r n r reason u; do
  printf '%-52s %-6s %-20s %s\n' "$n" "$r" "$reason" "$u"
done

PPI_BAD=$(jq '
  [
    .items[] |
    (.status.conditions // []) as $conds |
    ($conds | map(select(.type == "Ready" or .type == "ImageReady")) | .[0] // {}) as $c |
    select(
      ($c.status != "True")
      or (
        ($c.reason != "InfraEnvAvailable")
        and ((.status.imageUrl // "") == "")
      )
    )
  ] | length
' <<<"$PPI_JSON")
[[ "$PPI_BAD" -eq 0 && "$PPI_COUNT" -gt 0 ]] && pass "all PPIs Ready=True (InfraEnvAvailable / imageUrl set)" \
  || fail "$PPI_BAD PPI(s) not Ready with usable image"

PPI_NOLABEL=$(jq '[.items[]|select((.metadata.labels["infraenvs.agent-install.openshift.io"] // "")=="")]|length' <<<"$PPI_JSON")
[[ "$PPI_NOLABEL" -eq 0 ]] && pass "all PPIs have infraenv label" \
  || fail "$PPI_NOLABEL PPI(s) missing infraenvs.agent-install.openshift.io (assisted won't bind)"

# -----------------------------------------------------------------------------
hdr "3) InfraEnv + NMStateConfig selection"
IE_JSON=$(oc get infraenv -n "$NS" "$INFRAENV" -o json)
SELECTOR=$(jq -c '.spec.nmStateConfigLabelSelector.matchLabels // {}' <<<"$IE_JSON")
ISO_TYPE=$(jq -r '.spec.imageType // .spec.isoType // "?"' <<<"$IE_JSON")
CREATED=$(jq -r '.status.createdTime // "?"' <<<"$IE_JSON")
ISO_URL=$(jq -r '.status.isoDownloadURL // empty' <<<"$IE_JSON")
echo "imageType=$ISO_TYPE  createdTime=$CREATED"
echo "nmStateConfigLabelSelector=$SELECTOR"
[[ -n "$ISO_URL" ]] && pass "InfraEnv has isoDownloadURL" || fail "InfraEnv missing isoDownloadURL"
echo "isoDownloadURL=${ISO_URL:0:100}..."

NM_JSON=$(oc get nmstateconfig -n "$NS" -o json)
NM_COUNT=$(jq '.items|length' <<<"$NM_JSON")
echo "NMStateConfig count=$NM_COUNT"

# For each key in selector, count NMStateConfigs that match ALL keys
echo "NMStateConfig labels (live):"
jq -r '.items[]|"  \(.metadata.name)\t\(.metadata.labels)"' <<<"$NM_JSON"

MATCHED=$(jq --argjson sel "$SELECTOR" '
  [
    .items[]
    | . as $item
    | select(
        all(
          $sel | to_entries[]
          | ($item.metadata.labels[.key] // "") == .value
        )
      )
  ] | length
' <<<"$NM_JSON")
echo "NMStateConfigs matching selector: $MATCHED / $NM_COUNT"
if [[ "$MATCHED" -eq 0 ]]; then
  fail "ZERO NMStateConfigs match InfraEnv selector $SELECTOR — ISO will be DHCP-only"
elif [[ "$MATCHED" -lt "$NM_COUNT" ]]; then
  warn "only $MATCHED/$NM_COUNT NMStateConfigs match selector — missing label on:"
  jq -r --argjson sel "$SELECTOR" '
    .items[]
    | . as $item
    | select(
        any(
          $sel | to_entries[]
          | ($item.metadata.labels[.key] // "") != .value
        )
      )
    | "    \(.metadata.name)  labels=\(.metadata.labels)"
  ' <<<"$NM_JSON"
else
  pass "all NMStateConfigs match InfraEnv selector"
fi

# MAC overlap BMH bootMAC vs NMState
echo
echo "BMH bootMAC ↔ NMStateConfig macAddress:"
while IFS=$'\t' read -r bmh mac; do
  hit=$(jq -r --arg m "$mac" '
    [.items[] | select(
      (.spec.interfaces // []) | any((.macAddress // "" | ascii_downcase) == ($m | ascii_downcase))
    ) | .metadata.name] | join(",")
  ' <<<"$NM_JSON")
  if [[ -z "$hit" ]]; then
    fail "BMH $bmh MAC $mac has NO NMStateConfig"
  else
    pass "BMH $bmh MAC $mac → $hit"
  fi
done < <(jq -r '.items[]|[.metadata.name, .spec.bootMACAddress]|@tsv' <<<"$BMH_JSON")

# -----------------------------------------------------------------------------
hdr "4) BMO / ironic (metal3)"
if oc -n "$METAL3_NS" get deploy metal3-baremetal-operator >/dev/null 2>&1; then
  pass "metal3-baremetal-operator deploy exists in $METAL3_NS"
else
  fail "no metal3-baremetal-operator in $METAL3_NS"
fi

BMO_LOG=$(oc -n "$METAL3_NS" logs deploy/metal3-baremetal-operator --since=15m 2>/dev/null || true)
if [[ -z "$BMO_LOG" ]]; then
  warn "no BMO logs in last 15m"
else
  DF=$(grep -c 'deploy failed' <<<"$BMO_LOG" || true)
  NF=$(grep -c 'not found' <<<"$BMO_LOG" || true)
  PPIU=$(grep -c 'using PreprovisioningImage' <<<"$BMO_LOG" || true)
  echo "last 15m: deploy_failed_mentions=$DF  not_found=$NF  using_PPI=$PPIU"
  if [[ "$DF" -gt 0 ]]; then
    fail "BMO still seeing ironic 'deploy failed' — flap will NOT fix; recreate BMHs or clear ironic nodes"
    grep -E 'deploy failed|Resource .* not found' <<<"$BMO_LOG" | tail -5 | sed 's/^/  /'
  else
    pass "no 'deploy failed' in BMO logs (15m)"
  fi
  if grep -q 'Systems/.* not found' <<<"$BMO_LOG"; then
    fail "BMO: rf2vc Systems UUID not found (mapping or BIOS UUID stale)"
  fi
fi

# -----------------------------------------------------------------------------
hdr "5) rf2vc reachability + per-BMH System GET"
# resolve host + creds
if [[ -z "$RF2VC_HOST" ]]; then
  RF2VC_HOST=$(jq -r '
    .items[0].spec.bmc.address
    | sub("^redfish(-virtualmedia)?://";"")
    | split("/")[0]
  ' <<<"$BMH_JSON")
fi
echo "rf2vc host: $RF2VC_HOST"

if ! oc -n "$NS" get secret "$RF2VC_SECRET" >/dev/null 2>&1; then
  fail "secret $NS/$RF2VC_SECRET missing"
  USER=""; PASS=""
else
  USER=$(oc -n "$NS" get secret "$RF2VC_SECRET" -o jsonpath='{.data.username}' | base64 -d)
  PASS=$(oc -n "$NS" get secret "$RF2VC_SECRET" -o jsonpath='{.data.password}' | base64 -d)
  pass "loaded BMH BMC secret $RF2VC_SECRET"
fi

if [[ -n "${USER:-}" ]]; then
  # ServiceRoot (often no auth) + Systems collection
  CODE=$(curl -sk -o /tmp/rf2vc-root.json -w '%{http_code}' "https://$RF2VC_HOST/redfish/v1/" || echo 000)
  [[ "$CODE" == "200" ]] && pass "GET /redfish/v1/ → $CODE" || fail "GET /redfish/v1/ → $CODE"

  MAP_CODE=$(curl -sk -u "$USER:$PASS" -o /tmp/rf2vc-maps.json -w '%{http_code}' \
    "https://$RF2VC_HOST/api/v1/mappings" || echo 000)
  if [[ "$MAP_CODE" == "200" ]]; then
    MAP_N=$(jq 'if type=="array" then length else (.mappings // . | length) end' /tmp/rf2vc-maps.json 2>/dev/null || echo 0)
    pass "GET /api/v1/mappings → $MAP_CODE (count≈$MAP_N)"
  else
    fail "GET /api/v1/mappings → $MAP_CODE"
  fi

  echo
  printf '%-52s %-36s %-4s %s\n' BMH SYSTEM_UUID HTTP BODY
  while IFS=$'\t' read -r bmh addr; do
    uuid=$(sed -n 's|.*/Systems/\([^/]*\).*|\1|p' <<<"$addr")
    if [[ -z "$uuid" ]]; then
      fail "$bmh: cannot parse System UUID from $addr"
      continue
    fi
    body=$(mktemp)
    code=$(curl -sk -u "$USER:$PASS" -o "$body" -w '%{http_code}' \
      "https://$RF2VC_HOST/redfish/v1/Systems/$uuid" || echo 000)
    snippet=$(head -c 120 "$body" | tr '\n' ' ')
    printf '%-52s %-36s %-4s %s\n' "$bmh" "$uuid" "$code" "$snippet"
    if [[ "$code" == "200" ]]; then
      pass "$bmh System GET ok"
    else
      fail "$bmh System GET $code — $snippet"
    fi
    rm -f "$body"
  done < <(jq -r '.items[]|[.metadata.name,.spec.bmc.address]|@tsv' <<<"$BMH_JSON")
fi

# -----------------------------------------------------------------------------
hdr "6) rf2vc pod / recent activity"
if oc -n "$RF2VC_NS" get deploy rf2vc >/dev/null 2>&1; then
  pass "rf2vc deploy in $RF2VC_NS"
  oc -n "$RF2VC_NS" get pod -l app=rf2vc -o wide 2>/dev/null || true
  if oc -n "$RF2VC_NS" exec deploy/rf2vc -- sh -c 'test -s /data/state.json' 2>/dev/null; then
    SZ=$(oc -n "$RF2VC_NS" exec deploy/rf2vc -- wc -c /data/state.json 2>/dev/null | awk '{print $1}')
    pass "/data/state.json present (${SZ:-?} bytes)"
  else
    fail "/data/state.json missing or empty on rf2vc pod (mappings lost on rollout?)"
  fi
  echo "recent rf2vc log hits:"
  oc -n "$RF2VC_NS" logs deploy/rf2vc --since=10m 2>/dev/null \
    | grep -iE 'InsertMedia|not mapped|not found|vm-lookup|system-get|ERROR|resolve' \
    | tail -15 | sed 's/^/  /' || echo "  (none matched)"
else
  fail "no rf2vc deploy in $RF2VC_NS"
fi

# -----------------------------------------------------------------------------
hdr "7) Assisted (quick)"
if oc -n "$ASSISTED_NS" get deploy assisted-service >/dev/null 2>&1; then
  pass "assisted-service deploy exists"
  oc -n "$ASSISTED_NS" logs deploy/assisted-service --since=10m 2>/dev/null \
    | grep -iE 'PreprovisioningImage|deploy failed|mo-lab|waiting for update' \
    | tail -10 | sed 's/^/  /' || echo "  (no matching lines)"
else
  warn "assisted-service not found in $ASSISTED_NS"
fi

# -----------------------------------------------------------------------------
hdr "SUMMARY"
echo "FAILS=$FAILS  WARNS=$WARNS"
echo
echo "How to read:"
echo "  • FAIL on NMState selector/MAC     → fix labels/MACs, rebuild InfraEnv ISO, re-attach"
echo "  • FAIL on System GET / not found   → rf2vc mapping or BIOS UUID; fix before InsertMedia"
echo "  • FAIL on deploy failed in BMO     → ironic node poisoned; detach+strip finalizers+delete BMH, Argo sync"
echo "  • PASS System GET + still preparing + deploy failed → power path OK, provision state bad (recreate BMH)"
echo "  • PASS everything + preparing      → watch rf2vc for InsertMedia; check ironic conductor logs next"
echo
if [[ "$FAILS" -gt 0 ]]; then
  exit 1
fi
exit 0
