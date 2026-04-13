# create example

This example demonstrates creating a VM.

What it does:

- Reads `SLICER_URL`, `SLICER_TOKEN`, optional `SLICER_HOST_GROUP`, and optional `SLICER_CREATE_TAG` from the environment
- Calls `CreateVMWithOptions` and blocks until the guest agent is ready
- Tags the VM with `example=create-<unix timestamp>` unless `SLICER_CREATE_TAG` is set
- Prints the created VM hostname, IP, tag, timestamp, and architecture

Run:

```bash
SLICER_URL=http://192.168.1.34:8080 \
SLICER_TOKEN=$(cat ~/slicer-token.txt) \
go run .
```

Run from this directory. The example module requires `github.com/slicervm/sdk v0.0.42` and uses `replace github.com/slicervm/sdk => ../../` so local SDK changes are used.

For a local Unix socket, `SLICER_TOKEN` can be omitted.

Tags use the `key=value` form.
