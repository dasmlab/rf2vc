# rf2vc — Redfish → vSphere gateway

Public **Redfish BMC facade** in front of one or more **vCenters**, with a UUID→vCenter
map so ACM/MCE BareMetalHost (`redfish-virtualmedia://.../Systems/<BIOS-UUID>`) routes
to the right inventory.

| | |
|---|---|
| Source | https://github.com/dasmlab/rf2vc |
| Image | `ghcr.io/dasmlab/rf2vc:<tag>` (public — no pull secret) |

```
Admin UI ──► /data/state.json (PVC) ──► in-memory map
BMH/Ironic ──Redfish──► rf2vc ──govmomi──► vCenter A / B / …
```

## Deploy on OpenShift

```bash
# 1) Auth secret (required)
export RF2VC_AUTH_PASSWORD='pick-a-strong-password'
./scripts/bootstrap-secrets.sh

# 2) Optional: set image tag / PVC storageClass / route host
#    edit deploy/openshift/kustomization.yaml (images.newTag)
#    edit deploy/openshift/pvc.yaml if you need a specific StorageClass
#    edit deploy/openshift/route.yaml to set an explicit host

# 3) Apply
oc apply -k deploy/openshift/

# 4) Watch
oc -n rf2vc-system get pods,route
oc -n rf2vc-system get route rf2vc -o jsonpath='{.spec.host}{"\n"}'
```

Or apply the example secret from the template:

```bash
cp deploy/openshift/secret.example.yaml /tmp/rf2vc-secret.yaml
# edit auth-password
oc apply -f /tmp/rf2vc-secret.yaml
oc apply -k deploy/openshift/
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
- `GET /api/v1/vcenters/{id}/iso-status`
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
deploy/openshift/      portable OCP manifests (kustomize)
k8s_envelope/          dasmlab GitOps envelope + Argo Application
```
