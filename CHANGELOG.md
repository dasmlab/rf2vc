# Changelog

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
