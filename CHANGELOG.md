# Changelog

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
