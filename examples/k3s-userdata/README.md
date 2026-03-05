# k3s-userdata example

This example demonstrates creating a VM, installing k3s via user-data, waiting for cluster readiness, and exporting kubeconfig.

What it does:

- Creates a VM in the `vm` host group with `k3sup` install/userdata
- Tags the VM with `K3S_TAG` (or timestamp fallback)
- Waits for the agent to become healthy
- Polls `kubectl get nodes` with UID 1000
- Copies kubeconfig back locally and rewrites `127.0.0.1`/`localhost` to the VM IP

Run:

```bash
SLICER_URL=~/slicer-mac/slicer.sock \
go run ./examples/k3s-userdata
```

The example probes `slicer` via the SDK `/info` endpoint. If the daemon reports `platform=darwin`, it always uses the `sbox` hostgroup.

Optional overrides:

- For remote authenticated slicer instances, add `SLICER_TOKEN`.
- `SLICER_HOST_GROUP` (default: `vm`)
- `K3S_TAG` (default: `k3s-<unix timestamp>`)
- `SLICER_NODE_IP` (optional override if API-provided IP parsing fails)

Output includes:

- the direct kubeconfig path, e.g. `./kubeconfig-vm-1.yaml`
- a ready-to-run snippet:

```bash
KUBECONFIG=./kubeconfig-vm-1.yaml kubectl get nodes
```
