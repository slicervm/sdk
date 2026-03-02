# transform example

This example demonstrates copy-in, process-on-VM, copy-out.

What it does:

- Creates a VM
- Writes an input text file on the local machine
- Copies it to `/home/ubuntu/input.txt` inside the VM
- Runs a transform (`lowercase -> uppercase`) on the VM
- Copies `/home/ubuntu/output.txt` back to `./output-<vm-name>.txt`

Run:

```bash
SLICER_URL=http://192.168.1.34:8080 \
SLICER_TOKEN=$(cat ~/s9-token) \
go run ./examples/file-transfer
```

You can customize the input with:

- `FILE_TRANSFER_INPUT` — inline input string
- `FILE_TRANSFER_TAG` — VM tag
- `FILE_TRANSFER_OUTPUT` — output filename for an additional copy

Optional environment:

- `SLICER_HOST_GROUP` (default: `vm`)
