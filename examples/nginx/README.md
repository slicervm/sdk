# nginx + port-forward (Go)

Minimal port-forward demo for the Slicer Go SDK: spin up a VM, install nginx via userdata, expose port 80 on the host as `127.0.0.1:8080`, and fetch the welcome page through the tunnel — all from a single Go program.

What this exercises:

- **`CreateVMWithOptions` + `SlicerCreateNodeWaitUserdata`** — the daemon blocks server-side until apt-install finishes, so the next line can fetch immediately.
- **`demux.NewLocalClient`** from `github.com/openfaasltd/inlets-pro/pkg/demux` — the same library `slicer vm forward` uses, so behaviour matches the CLI exactly.
- **Plain `http.Get`** from the host — confirms the bytes round-trip cleanly.

The TypeScript equivalent is at [`@slicervm/sdk` examples/nginx](https://github.com/slicervm/sdk-ts/tree/master/examples/nginx).

## Run

```bash
go run .
```

With overrides:

```bash
SLICER_URL=~/slicer-mac/slicer.sock go run .
SLICER_URL=https://slicer.example.com SLICER_TOKEN=$(sudo cat /var/lib/slicer/auth/token) go run .
```

Expected output:

```
→ creating VM in "sbox" with nginx userdata (blocking on wait=userdata)…
  VM sbox-1 (192.168.64.3) ready in 6.2s
→ opening forward 127.0.0.1:8080 → VM:80…
→ GET http://127.0.0.1:8080/
  status=200 bytes=615
  ✓ welcome page served end-to-end via port-forward
→ deleting VM sbox-1…
done in 6.4s
```

## Notes

- First run may take longer if `sbox-base.img` is being built from OCI on the host.
- For repeat runs, [build a custom image](https://docs.slicervm.com/platform/custom-images/) with nginx pre-installed.
