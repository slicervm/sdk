# transform example

This example demonstrates copy-in, process-on-VM, copy-out.

What it does:

- Creates a VM and blocks until the guest agent is ready
- Tags the VM with `example=transform-<unix timestamp>` unless `FILE_TRANSFER_TAG` is set
- Writes an input text file on the local machine
- Copies it to `/home/ubuntu/input.txt` inside the VM
- Runs a transform (`lowercase -> uppercase`) on the VM
- Copies `/home/ubuntu/output.txt` back to `./output-<vm-name>.txt`

Run:

```bash
SLICER_URL=http://192.168.1.34:8080 \
SLICER_TOKEN=$(cat ~/slicer-token.txt) \
go run .
```

Run from this directory. The example module requires `github.com/slicervm/sdk v0.0.42` and uses `replace github.com/slicervm/sdk => ../../` so local SDK changes are used.

You can customize the input with:

- `FILE_TRANSFER_INPUT` — inline input string
- `FILE_TRANSFER_TAG` — VM tag in `key=value` form
- `FILE_TRANSFER_OUTPUT` — output filename for an additional copy

Optional environment:

- `SLICER_HOST_GROUP` (default: `vm`)
