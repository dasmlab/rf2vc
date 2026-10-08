# Changelog

## [1.3.0] — 2026-10-08

- **vCenters from ConfigMaps**: at startup the gateway loads every ConfigMap labelled `rf2vc.dasmlab.org/vcenter=true` (key `vcenter.yaml`) in its namespace, so vCenters can be defined from GitOps / ACM policy. ConfigMap fields win; existing vCenters are matched by id or name, keeping their UUID bindings.
- **Password via Secret reference** (`credentialsSecret`, for VSO later); until that Secret exists, the password can still be typed in the UI and is kept across restarts.
- **UI-defined vCenters are written to ConfigMaps**: creating or editing a vCenter in the UI (or finding one only on the PVC at startup) writes `rf2vc-vc-<name>` labelled `rf2vc.dasmlab.org/origin=runtime`, without the password; deleting it in the UI removes the ConfigMap. GitOps-owned ConfigMaps are never written, and their vCenters cannot be deleted from the UI.
- UI shows where each vCenter is defined (GitOps / runtime ConfigMap) and where its password comes from; vCenters without a password are tagged in the list.
- A vCenter without a password is never logged into (health, folder scan, Redfish calls report "no password set"), so a ConfigMap waiting for its Secret cannot lock the vCenter account with failed logins.
- New Role/RoleBinding `rf2vc-vcenter-config` for `rf2vc-sa`; example in `deploy/openshift/vcenter.example.yaml`.

## [1.2.3] — 2026-10-03

- **EN | FR-CA (Québec French)**: language toggle in the dashboard header and on the oauth sign-in page; the choice is remembered in the browser and shared between both (French browsers default to FR-CA). Covers inventory, vCenter panel, health checks, folder status, UUID rows, ISO cache, forms, confirmations, Activity and the Test checklist (gateway messages such as "connected · …" / "authenticated as …" are translated client-side). Breakglass is **Bris de glace** in French.
- Switching language re-renders the current view in place; an open create/edit form keeps what was typed.

## [1.2.2] — 2026-10-03

- **Sign-in page: breakglass behind its own button**: the oauth-proxy page now shows only **Log in with OpenShift** plus a **Breakglass** button; the local username/password form is a separate view (`#breakglass`) with an emergency-use warning and a link back. A failed breakglass attempt stays on that view.
- Templates (`web/oauth/sign_in.html`, `error.html`) ship in the rf2vc image; the existing init container writes them with `-write-oauth-templates` into an `emptyDir`, and oauth-proxy loads them via `--custom-templates-dir`.

## [1.2.1] — 2026-10-03

- **Activity order toggle**: toolbar button switches between newest first (default) and oldest first; the choice is remembered in the browser. New lines arrive at the top in newest-first mode without jumping a reader who has scrolled down. Overlapping polls no longer render the same event twice.
- **vCenter errors stand out**: failed health checks, folder-scan failures and error messages in the vCenter panel are shown big, bold and red inside a red rounded box. A failed login/datacenter (e.g. expired vSphere session) is now marked ✗ instead of ✓ — the probe reports it as yellow, which the UI used to count as passing.
- **Folder status line (UUIDs tab) is always boxed**: teal when the scan is OK, amber for "Folder not set" / no VMs, red for any scan failure — this is where a stale vSphere session (`NotAuthenticated`) shows up at runtime, while the health checks still log in fresh and stay green.
- **Test checklist splits reachability from login**: new `vCenter endpoint` check (vSphere API answers, shows host + vCenter version, no credentials used) before `Login (credentials)` ("authenticated as <user>"). An unreachable vCenter now fails the endpoint check and skips login instead of reporting a login failure. Missing form fields are reported as `Settings`.

## [1.2.0] — 2026-09-29

- **System callers on `/api` use ServiceAccount tokens**: new TLS listener `:8444` (`apiListen` / `RF2VC_API_LISTEN`, service-ca cert, Service port `api`). Tokens are checked with SelfSubjectReview + SelfSubjectAccessReview using the caller's own token against Role `rf2vc-ui-access` (no `system:auth-delegator` needed). ServiceAccount `rf2vc-api-client` + RoleBinding `rf2vc-api-clients` added.
- **Per-BMH Redfish credentials**: optional Secret `rf2vc-redfish-clients` (key = username, value = password), mounted at `RF2VC_REDFISH_CLIENTS_DIR` and re-read for rotation without restart. The shared account still works on `/redfish` (with a warning in Activity) until `RF2VC_REDFISH_DISABLE_SHARED=true`.
- Redfish inbound activity records the `client`; token API changes are logged as `api <METHOD> <path>` with the ServiceAccount.

## [1.1.0] — 2026-09-29

- **Dashboard login via the cluster IdP**: OpenShift oauth-proxy sidecar in front of the dashboard + `/api`, gated by RBAC (`rf2vc-ui-access` → Group `tdm-chips-admin`). The `rf2vc-gateway` Secret account stays as break-glass on the proxy sign-in page.
- Gateway can serve the dashboard/API on a separate loopback listener (`uiListen` / `RF2VC_UI_LISTEN`); the main listener then serves only Redfish with Basic Auth.
- Routes split on one host: `rf2vc` (UI, reencrypt) and `rf2vc-redfish` (`path: /redfish`, Basic Auth). BMH addresses and credentials are unchanged.
- `GET /api/v1/whoami`, signed-in user + sign-out in the header, UI changes logged with the user.

## [1.0.34] — 2026-09-25

- **EjectMedia**: on CD lock, set VMware force-unlock ExtraConfig (`cdrom.showIsoLockWarning=FALSE` + `msg.autoAnswer`) and retry detach **before** any power cycle; soft-off timeout 45s. Aims to eject while the guest stays up so Ironic's post-eject reboot is clean.

## [1.0.33] — 2026-09-25

- **EjectMedia**: on CD lock, prefer guest soft-shutdown (then hard PowerOff only if needed), detach + disk-first boot, then **restore prior power** (no longer leave the VM off). Avoids yanking power under a live rootfs ("Structure needs cleaning") and the stuck-off after eject.

## [1.0.32] — 2026-09-25

- **EjectMedia**: if CD disconnect fails while the guest is up (`Connection control operation failed for disk 'sata0:0'`), power off, detach, leave powered off (Ironic powers back on). Always restore **disk-first** boot order after eject.
- **Boot PATCH**: honor `BootSourceOverrideEnabled=Disabled` / `Target=Hdd|Disk|None` → disk-first boot (previously only CD was handled, so post-install reboot kept preferring the ISO).
- Add `scripts/triage-mo-lab-provisioning.sh` for BMH/PPI/InfraEnv/rf2vc System GET triage.

## [1.0.0] — 2026-09-16

First public release.

- Multi-vCenter UUID map on PVC with admin UI
- Redfish surface for BMH `redfish-virtualmedia` (Systems, Reset, Boot CD, VirtualMedia)
- ISO datastore cache (hash URL, skip UploadFile when present)
- Portable OpenShift manifests under `deploy/openshift/` (no pull-secret)
- Public image `ghcr.io/dasmlab/rf2vc:v1.0.0`
- Architecture diagrams (D2 → SVG) and scorecard in `docs/ARCHITECTURE.md`
