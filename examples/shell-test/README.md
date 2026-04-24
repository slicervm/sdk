# shell-test

Minimal browser-based terminal for Slicer VMs, using the SDK's `shell.ProxyHandler` and xterm.js. No auth, no users — for integration testing only.

## Prerequisites

- A running Slicer daemon with at least one host group configured.
- Go 1.22+

## Quick start

### 1. Generate a Slicer config and start the daemon

```bash
mkdir -p /tmp/shell-test && cd /tmp/shell-test

slicer new mygroup \
  --count=0 \
  --net=isolated \
  --api-bind=./shell-test.sock \
  --graceful-shutdown=false \
  > slicer.yaml

sudo -E slicer up ./slicer.yaml > ./slicer.log 2>&1 &
echo $! | sudo tee /tmp/shell-test/slicer.pid
```

Wait a few seconds for the daemon to start, then verify:

```bash
tail -5 /tmp/shell-test/slicer.log
# Should show "API listening on Unix socket: ./shell-test.sock"
```

### 2. Start the shell-test UI

```bash
cd /path/to/sdk
go run ./examples/shell-test/ --url /tmp/shell-test/shell-test.sock
```

Open **http://\<host\>:3333** in a browser.

If no VMs are running, click **Launch VM** in the UI. Otherwise, select a VM from the dropdown and click **Connect**.

### 3. Clean up

```bash
sudo kill -INT $(cat /tmp/shell-test/slicer.pid)
sleep 3
sudo kill -9 $(cat /tmp/shell-test/slicer.pid) 2>/dev/null || true
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--url` | *(required)* | Slicer API URL or unix socket path |
| `--token` | | Bearer token (optional, not needed for unix sockets) |
| `--listen` | `0.0.0.0:3333` | HTTP listen address |
| `--shell` | `/bin/bash` | Shell binary inside the VM |

## How it works

The Go server provides three API endpoints:

- `GET /api/vms` — lists running VMs via `client.ListVMs()`
- `GET /api/hostgroups` — lists host groups via `client.GetHostGroups()`
- `POST /api/launch?group=NAME` — creates a new VM via `client.CreateVMWithOptions()`, waits for agent readiness

The shell WebSocket is proxied at `/ws/shell?vm=HOSTNAME` using `shell.NewProxyHandler()` with a `VMNameFunc` that reads the hostname from the query string.

The frontend uses CDN-hosted xterm.js and the Slicer binary frame protocol (5-byte header: 1 byte type + 4 bytes big-endian length).
