# create example

This example demonstrates creating a VM.

What it does:

- Reads `SLICER_URL` and `SLICER_TOKEN` from the environment
- Calls `CreateVM` in the `vm` host group
- Prints the created VM hostname, IP, timestamp, and architecture

Run:

```bash
SLICER_URL=http://192.168.1.34:8080 \
SLICER_TOKEN=$(cat ~/s9-token) \
go run ./examples/create
```

