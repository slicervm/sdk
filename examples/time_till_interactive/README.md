# time_till_interactive example

This example measures:

- creates a VM with `CreateVMWithOptions` (server-side readiness via `wait=agent` by default)
- requests 1 vCPU / 1 GiB RAM for faster TTI-style measurements
- waits until the VM is ready (`tti`)
- tags the VM with `example=tti-<unix timestamp>` unless `SLICER_TTI_TAG` is set
- runs `echo ready` using `ExecBuffered` and measures first-command latency
- logs create, exec, and cleanup progress
- uses the SDK's default `stdio=base64` exec mode; the SDK decodes stdout
  before returning it
- Prints:
  - `create_to_ready`
  - `exec_latency`

Recommended local e2e setup (copy/paste):

```bash
./bin/slicer new timetti \
  --min \
  --count=0 \
  --storage devmapper \
  --net=isolated \
  --api-bind /tmp/slicer-e2e.sock \
  > /tmp/slicer-e2e.yaml

sudo -E ./bin/slicer up /tmp/slicer-e2e.yaml > /tmp/slicer-e2e.log 2>&1 &
```

Run the example against the unix socket:

```bash
SLICER_URL=/tmp/slicer-e2e.sock \
SLICER_HOST_GROUP=timetti \
SLICER_CREATE_WAIT=agent \
go run .
```

Run from this directory. The example module requires `github.com/slicervm/sdk v0.0.42` and uses `replace github.com/slicervm/sdk => ../../` so local SDK changes are used.

Optional env:

- `SLICER_CREATE_TIMEOUT` (duration, e.g. `120s`, default `3m`)
- `SLICER_CREATE_WAIT` (`agent` or `userdata`)
- `SLICER_TTI_TAG` (default: `example=tti-<unix timestamp>`, use `key=value` form)

## Sample run data

Observed on this host:

- CPU: `AMD Ryzen 9 9950X3D 16-Core Processor` (`32` logical CPUs, `16` cores, SMT enabled)

Command:

```bash
SLICER_URL=/tmp/tti-18248.sock \
SLICER_HOST_GROUP=tti-18248 \
SLICER_TOKEN="" \
SLICER_CREATE_TIMEOUT=120s \
go run ./examples/time_till_interactive
```

Output:

```text
created vm=tti-18248-1 in=510.510513ms
tti=510.510513ms
exec_latency=5.975071ms
create_to_ready=510.510513ms output="ready"
deleted vm=tti-18248-1
```
