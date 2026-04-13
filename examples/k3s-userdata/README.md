# k3s-userdata example

This example demonstrates creating a VM, installing k3s via user-data, waiting for cluster readiness, and exporting kubeconfig.

What it does:

- Creates a 2 vCPU / 4 GiB VM in the `vm` host group with `k3sup` install/userdata and blocks until userdata is complete
- Tags the VM with `example=k3s-<unix timestamp>` unless `K3S_TAG` is set
- Verifies `kubectl get nodes` with UID 1000 after server-side userdata readiness returns
- Logs progress and includes VM log excerpts if readiness is slow or fails
- Copies kubeconfig back locally and rewrites `127.0.0.1`/`localhost` to the VM IP

Run:

```bash
SLICER_URL=~/slicer-mac/slicer.sock \
go run .
```

Run from this directory. The example module requires `github.com/slicervm/sdk v0.0.42` and uses `replace github.com/slicervm/sdk => ../../` so local SDK changes are used.

The example probes `slicer` via the SDK `/info` endpoint. If the daemon reports `platform=darwin`, it always uses the `sbox` hostgroup.

Optional overrides:

- For remote authenticated slicer instances, add `SLICER_TOKEN`.
- `SLICER_HOST_GROUP` (default: `vm`)
- `K3S_TAG` (default: `example=k3s-<unix timestamp>`, use `key=value` form)
- `SLICER_NODE_IP` (optional override if API-provided IP parsing fails)

Output includes:

- the direct kubeconfig path, e.g. `./kubeconfig-vm-1.yaml`
- a ready-to-run snippet:

```bash
KUBECONFIG=./kubeconfig-vm-1.yaml kubectl get nodes
```
