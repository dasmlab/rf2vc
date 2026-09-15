# rf2vc — Redfish → vSphere gateway

Minimal **Redfish BMC facade** in front of **vCenter** so ACM/MCE BareMetalHost
(`redfish-virtualmedia://...`) can power VMs and attach discovery ISOs.

```
ACM / BMO  --Redfish-->  rf2vc  --govmomi-->  vCenter  -->  VMs
```

vCenter does **not** expose `/redfish/v1` for guest VMs. This service does.

## Prod (2026-prod-1)

| Item | Value |
|---|---|
| Route | `https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org` |
| Namespace | `rf2vc-system` |
| Image | `ghcr.io/dasmlab/rf2vc:<version>` |
| GitOps | `dasmlab-live-cicd` → `clusters/2026-prod-1/rf2vc/live` |
| Secrets | `rf2vc-gateway` (auth + vSphere) via `scripts/bootstrap-secrets.sh` |

BMH address after deploy:

```yaml
bmc:
  address: "redfish-virtualmedia://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/redfish/v1/Systems/<BIOS-UUID>"
  credentialsName: <secret matching RF2VC_AUTH_*>
  disableCertificateVerification: true   # or trust the HAP/LE cert
```

## Local / export build

```bash
cp configs/gateway.example.yaml configs/gateway.yaml
# edit gateway.yaml — vCenter, datastore, auth

go mod tidy
make build
./bin/rf2vc -config configs/gateway.yaml
```

Container:

```bash
buildah bud -f deployments/containers/Containerfile -t rf2vc:dev .
# or: docker build -f Dockerfile -t rf2vc:dev .
```

## Smoke test

```bash
curl -fsS https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/healthz
curl -fsS -u 'redfish:PASSWORD' https://rf2vc.apps.2026-prod-1.ocp.dasmlab.org/redfish/v1/
```

## Layout

```
cmd/gateway/                 main
internal/config/             YAML + RF2VC_* env overlays
internal/redfish/            Redfish HTTP surface (+ /healthz)
internal/vsphere/            govmomi power + ISO attach (lazy connect)
configs/                     example config
deployments/containers/      Containerfile (CI)
k8s_envelope/                OCP deploy + Argo Application
scripts/ci/                  GH Actions GitOps + HAP CERTX helpers
.github/workflows/main.yml   build → GHCR → live-cicd
```

## Lab caveats

- Not a full Redfish implementation — only what BMO needs for this flow
- ISO staging needs datastore capacity and network path to the Assisted image service
- vSphere credentials stay in `rf2vc-gateway` Secret (never in GitOps YAML)
- Prefer edge TLS at OpenShift Route / HAProxy; set `tlsCertFile`/`tlsKeyFile` only for standalone export
