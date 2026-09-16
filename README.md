# rf2vc — Redfish → vSphere gateway

Public **Redfish BMC facade** in front of one or more **vCenters**, with a UUID→vCenter
map so ACM/MCE BareMetalHost (`redfish-virtualmedia://.../Systems/<BIOS-UUID>`) routes
to the right inventory.

![rf2vc overview](diagrams/rf2vc-overview.svg)

| | |
|---|---|
| Source | https://github.com/dasmlab/rf2vc |
| Image | `ghcr.io/dasmlab/rf2vc:latest` / `vX.Y.Z-<sha>` (public — no pull secret) |
| Docs | [Architecture](docs/ARCHITECTURE.md) · [OpenShift deploy](deploy/openshift/) |

```
Admin UI ──► /data/state.json (PVC) ──► in-memory map
BMH/Ironic ──Redfish──► rf2vc ──govmomi──► vCenter A / B / …
```

## How it works

**Control path** — ACM posts Redfish actions against `/redfish/v1/Systems/{UUID}`. rf2vc
looks up the UUID, selects the mapped vCenter credentials, finds the VM by BIOS UUID,
then applies power / boot / virtual-media via govmomi.

**ISO path** — `imageSetRef` stays in ACM/Assisted. Ironic sends an ISO URL in
`InsertMedia`; rf2vc hashes it, reuses or uploads to the datastore, and attaches CDROM.

Details + scorecard: **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**

![control path](diagrams/rf2vc-control-path.svg)

## Deploy on OpenShift

```bash
git clone https://github.com/dasmlab/rf2vc.git && cd rf2vc
git checkout v1.0.0

export RF2VC_AUTH_PASSWORD='pick-a-strong-password'
./scripts/bootstrap-secrets.sh

# optional: StorageClass in deploy/openshift/pvc.yaml ; route host auto-assigned if unset
oc apply -k deploy/openshift/

oc -n rf2vc-system get pods,route
```

BMH example (replace host + secret):

```yaml
bmc:
  address: "redfish-virtualmedia://rf2vc.apps.<cluster>/redfish/v1/Systems/<BIOS-UUID>"
  credentialsName: <secret matching rf2vc auth>
  disableCertificateVerification: true
```

## Local

```bash
cp configs/gateway.example.yaml configs/gateway.yaml
# set auth; mkdir -p data
make build && make run
# UI: http://127.0.0.1:8080/  (basic auth)
```

## API (basic auth)

- `GET /api/v1/status`
- `GET|POST /api/v1/vcenters`
- `GET|PUT|DELETE /api/v1/vcenters/{id}`
- `POST /api/v1/vcenters/{id}/test`
- `GET /api/v1/vcenters/{id}/iso-status`
- `GET|POST /api/v1/mappings`
- `PUT|DELETE /api/v1/mappings/{uuid}`

## Diagrams

Sources are D2 under `diagrams/*.d2`. CI renders sibling SVGs (same pipeline as other
dasmlab projects). Locally: `d2 diagrams/rf2vc-overview.d2 diagrams/rf2vc-overview.svg`

## Versioning

Every push to `main` auto-bumps **patch** SemVer (from git tags + `.localbuild`), publishes
`ghcr.io/dasmlab/rf2vc:vX.Y.Z-<short-sha>` and `:latest`, then tags `vX.Y.Z`.

- Draw a line: `workflow_dispatch` with bump `minor`/`major`, or commit message `[bump minor]` / `[bump major]`
- Local helper: `./commitme.sh point|minor|major "message"` (same pattern as other dasmlab repos)

## Layout

```
cmd/gateway/           main + auth mux
internal/store/        PVC JSON + UUID/vCenter indexes
internal/vsphere/      per-VC client + ISO cache
internal/redfish/      Redfish surface (map-routed)
internal/api/          management REST
web/                   embedded admin UI
deploy/openshift/      portable OCP manifests (kustomize)
diagrams/              D2 sources + rendered SVGs
docs/                  architecture notes
k8s_envelope/          dasmlab GitOps envelope
```
