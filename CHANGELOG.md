# Changelog

## [1.0.12] — 2026-09-25

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
