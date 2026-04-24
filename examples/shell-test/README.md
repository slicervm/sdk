# shell-test

Minimal React + xterm.js browser terminal for Slicer VMs, using the SDK's `shell.ProxyHandler`. No auth, no users — for integration testing and as a reference for building your own shell UI.

## Prerequisites

- A running Slicer daemon
- Go 1.22+
- Node.js 18+

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

Wait a few seconds, then verify:

```bash
tail -5 /tmp/shell-test/slicer.log
# Should show "API listening on Unix socket: ./shell-test.sock"
```

### 2. Build and start the shell-test UI

```bash
cd examples/shell-test/web
npm install
npm run build
cd ..

go run . --url /tmp/shell-test/shell-test.sock
```

Open **http://\<host\>:3333** in a browser.

No VMs will be running initially — click **Launch VM** to create one, then select it and click **Connect**.

### 3. Development mode

For live-reload during frontend development, run the Go server and Vite dev server separately:

```bash
# Terminal 1: Go API + WebSocket proxy
go run . --url /tmp/shell-test/shell-test.sock

# Terminal 2: Vite dev server with proxy to Go backend
cd web && npm run dev
```

Then open the Vite URL (usually http://localhost:5173). API and WebSocket requests are proxied to the Go server via `vite.config.ts`.

### 4. Clean up

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

## Architecture

The Go server provides:

- `GET /api/vms` — lists VMs via `client.ListVMs()`
- `GET /api/hostgroups` — lists host groups via `client.GetHostGroups()`
- `POST /api/launch?group=NAME` — creates a VM via `client.CreateVMWithOptions()`, waits for agent readiness
- `GET /ws/shell?vm=HOSTNAME` — WebSocket proxy via `shell.NewProxyHandler()` with `VMNameFunc`

The React frontend (`web/src/`) polls the VM list every 5 seconds, provides a dropdown to select a VM, and renders an xterm.js terminal connected via the Slicer binary frame protocol.

### Shell wire protocol

Each WebSocket message is a binary frame:

| Offset | Size | Field |
|--------|------|-------|
| 0 | 1 byte | Frame type |
| 1–4 | 4 bytes | Payload length (big-endian) |
| 5+ | variable | Payload |

Frame types: `0x01` Data, `0x02` WindowSize (8 bytes: cols + rows as uint32 BE), `0x03` Shutdown, `0x04` Heartbeat.

See `web/src/shell.ts` for the browser implementation.
