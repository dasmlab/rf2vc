# rf2vc — Redfish → vSphere gateway

Minimal **Redfish BMC facade** in front of one or more **vCenters**, with a UUID→vCenter
map so ACM/MCE BareMetalHost (`redfish-virtualmedia://.../Systems/<BIOS-UUID>`) routes
to the right inventory.

```
Admin UI ──► /data/state.json (PVC) ──► in-memory map
BMH/Ironic ──Redfish──► rf2vc ──govmomi──► vCenter A / B / …
```

## Prod (2026-prod-1)

| Item | Value |
|---|---|
| UI / API | `https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/` |
| Redfish | `https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/redfish/v1/` |
| Namespace | `rf2vc-system` |
| PVC | `rf2vc-data` → `/data/state.json` |
| Image | `ghcr.io/dasmlab/rf2vc:<version>` |

Open the UI (HTTP basic auth), add GOVC-shaped vCenters, bind BIOS UUIDs.

```yaml
bmc:
  address: "redfish-virtualmedia://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/redfish/v1/Systems/<BIOS-UUID>"
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

Optional first-boot seed:

```bash
export GOVC_URL=https://vcenter.example
export GOVC_USERNAME=...
export GOVC_PASSWORD=...
export GOVC_DATACENTER=...
export GOVC_DATASTORE=...
export GOVC_INSECURE=1
```

## API (basic auth)

- `GET /api/v1/status`
- `GET|POST /api/v1/vcenters`
- `GET|PUT|DELETE /api/v1/vcenters/{id}`
- `POST /api/v1/vcenters/{id}/test`
- `GET|POST /api/v1/mappings`
- `PUT|DELETE /api/v1/mappings/{uuid}`

## Layout

```
cmd/gateway/           main + auth mux
internal/store/        PVC JSON + in-memory UUID/vCenter indexes
internal/vsphere/      per-VC client + pool
internal/redfish/      Redfish surface (map-routed)
internal/api/          management REST
web/                   embedded admin UI (go:embed)
k8s_envelope/          OCP + PVC + Argo
```
