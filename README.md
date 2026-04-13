## Golang SDK for Slicer

SDK for [SlicerVM.com](https://slicervm.com)

## Table of Contents

- [Installation](#installation)
- [Features](#features)
- [Connecting to UNIX Sockets](#connecting-to-unix-sockets)
- [Port Forwarding with UNIX Sockets](#port-forwarding-with-unix-sockets)
- [Pause and Resume VMs](#pause-and-resume-vms)
- [SDK Methods Reference](#sdk-methods-reference)
  - [VM Operations](#vm-operations)
  - [Guest Operations](#guest-operations)
  - [Secret Management](#secret-management)
- [Documentation](#documentation)
- [Quick Start](#quick-start)

### Installation

```bash
go get github.com/slicervm/sdk@latest
```

### Features

- **VM Management**: Create, list, delete, pause, and resume VMs.
- **Execute Commands in VMs**: Run commands in VMs and stream command output (stdout/stderr).
- **File Management**: Upload and download files to/from VMs with `CpToVM` and `CpFromVM`.
- **Secret Management**: Securely manage secrets like API keys or other sensitive information for VMs.
- **Port Forwarding**: Bidirectional port forwarding with support for TCP ports and UNIX sockets.
- **Pause/Resume**: Pause VM CPU usage, then resume instantly when needed.

### Connecting to UNIX Sockets

The SDK supports connecting directly to the Slicer API via a UNIX socket. Simply pass the socket path as the `baseURL` when creating a client:

```go
// Connect via Unix socket
client := sdk.NewSlicerClient("/var/run/slicer/api.sock", token, userAgent, nil)

// Then use normally
ctx := context.Background()
hostGroups, err := client.GetHostGroups(ctx)
```

The client automatically detects UNIX socket paths (starting with `/` or `./`) and configures the HTTP transport accordingly.

### Port Forwarding with UNIX Sockets

Bidirectional port forwarding now supports UNIX sockets on either side or both. Forward local or remote TCP ports and UNIX sockets in any combination.

For now you need to use the CLI and an "exec" to access port-forwarding.

You can also use SSH tunnels via Go code, if you have SSH in your microVMs.

**Supported on**: Linux, Darwin, and WSL.

### Pause and Resume VMs

Control VM CPU usage with instant pause/resume:

```go
// Pause a VM
err := client.PauseVM(ctx, "vm-1")

// Resume a VM
err := client.ResumeVM(ctx, "vm-1")
```

### SDK Methods Reference

#### Key concepts

User-facing documentation may refer to Sandboxes and Services. These are logical concepts, not API primitives.

Primitives:

* `Host Group` - Host Groups define the template or specification for a Virtual Machine. A slicer daemon can have multiple host groups, but most use-cases should use only one. Host Groups are defined in YAML, and not via API at this time.
* `VM` - A virtual machine - (sometimes called Node). Theses are either launched via an initial `count` value per Host Group, or on demand via API.

When you want a "Sandbox" (read: disposable VM launched via API), it's recommended that you have `count: 0` in your host group. VMs launched via API are backed by a persistent disk or snapshot whilst running, which is removed when they terminate.

When you want to host a "Service" or run a server, such as a Kubernetes cluster, or a version of your application, it's best to write a succinct name for the host group such as "k3s" or "app" and then define how many VMs you'll need via `count: 1`, or `count: 3`, etc.

#### VM Operations

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `CreateVM(ctx, groupName, request)` | Create a new VM in a host group. The underlying `POST /hostgroup/{name}/nodes` endpoint accepts optional `?wait=agent` or `?wait=userdata` query params with a `?timeout=` (Go duration) to block until readiness; set `CreateOpts.Wait` / `CreateOpts.Timeout` on the request to use them. | `ctx` (context.Context), `groupName` (string), `request` (SlicerCreateNodeRequest) | (*SlicerCreateNodeResponse, error) |
| `RelaunchVM(ctx, hostname)` | Relaunch a known stopped persistent VM (re-uses its disk image). | `ctx` (context.Context), `hostname` (string) | (*SlicerCreateNodeResponse, error) |
| `DeleteVM(ctx, groupName, hostname)` | Delete a VM from a host group | `ctx` (context.Context), `groupName` (string), `hostname` (string) | (*SlicerDeleteResponse, error) |
| `ListVMs(ctx, opts...)` | List all VMs across all host groups. Pass an optional `ListOptions{Tag: "…"}` or `ListOptions{TagPrefix: "…"}` to filter server-side. | `ctx` (context.Context), `opts` (...ListOptions) | ([]SlicerNode, error) |
| `GetHostGroups(ctx)` | Fetch all host groups | `ctx` (context.Context) | ([]SlicerHostGroup, error) |
| `GetHostGroupNodes(ctx, groupName, opts...)` | Fetch nodes for a specific host group. Optional `ListOptions` filter works the same as `ListVMs`. | `ctx` (context.Context), `groupName` (string), `opts` (...ListOptions) | ([]SlicerNode, error) |
| `DeleteNode(groupName, nodeName)` | Delete a node from a host group | `groupName` (string), `nodeName` (string) | error |
| `PauseVM(ctx, hostname)` | Pause a running VM to save CPU cost | `ctx` (context.Context), `hostname` (string) | error |
| `ResumeVM(ctx, hostname)` | Resume a paused VM | `ctx` (context.Context), `hostname` (string) | error |
| `SuspendVM(ctx, hostname)` | Suspend a running VM to disk via a Firecracker snapshot. Memory and disk state are saved; the VM is shut down. **Slicer-for-Mac only, for now** — the Linux daemon will return `501 Not Implemented`. | `ctx` (context.Context), `hostname` (string) | error |
| `RestoreVM(ctx, hostname)` | Restore a VM from its previously-taken Firecracker snapshot. **Slicer-for-Mac only, for now.** | `ctx` (context.Context), `hostname` (string) | error |
| `Shutdown(ctx, hostname, request)` | Shutdown or reboot a VM | `ctx` (context.Context), `hostname` (string), `request` (*SlicerShutdownRequest) | error |
| `GetVMStats(ctx, hostname)` | Get CPU, memory, and disk statistics for a VM or all VMs | `ctx` (context.Context), `hostname` (string, empty for all) | ([]SlicerNodeStat, error) |
| `GetVMLogs(ctx, hostname, lines)` | Get recent logs from a VM | `ctx` (context.Context), `hostname` (string), `lines` (int, -1 for all) | (*SlicerLogsResponse, error) |
| `GetInfo(ctx)` | Fetch server version and build information | `ctx` (context.Context) | (*SlicerInfo, error) |

#### Guest Operations

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `Exec(ctx, hostname, request)` | Execute a command in a VM and stream output line-by-line as NDJSON frames. Typed frames (`started`, `stdout`, `stderr`, `exit`) let callers measure process-start latency separately from first-byte latency. | `ctx` (context.Context), `hostname` (string), `request` (SlicerExecRequest) | (chan SlicerExecWriteResult, error) |
| `ExecBuffered(ctx, hostname, request)` | Execute a command and wait for completion — a single buffered JSON result instead of the NDJSON stream. Use this when you don't need live output. Hits `POST /vm/{hostname}/exec?buffered=true`. `stdin` is not supported; use `ExecWithReader` for that. | `ctx` (context.Context), `hostname` (string), `request` (SlicerExecRequest) | (ExecResult, error) |
| `CpToVM(ctx, vmName, localPath, vmPath, uid, gid, permissions, mode)` | Upload a file/directory to a VM | `ctx` (context.Context), `vmName` (string), `localPath` (string), `vmPath` (string), `uid` (uint32), `gid` (uint32), `permissions` (string), `mode` (string: "tar" or "binary") | error |
| `CpFromVM(ctx, vmName, vmPath, localPath, permissions, mode)` | Download a file/directory from a VM | `ctx` (context.Context), `vmName` (string), `vmPath` (string), `localPath` (string), `permissions` (string), `mode` (string: "tar" or "binary") | error |

When `mode` is `tar`, `localPath` is treated as a directory destination and will be created automatically if it does not already exist.
| `GetAgentHealth(ctx, hostname, includeStats)` | Check VM agent health and optionally get system stats | `ctx` (context.Context), `hostname` (string), `includeStats` (bool) | (*SlicerAgentHealthResponse, error) |

#### Filesystem Operations

These methods hit native `/vm/{hostname}/fs/*` endpoints — preferred over
shelling out through `Exec` because they don't depend on guest-side
`ls`/`stat`/`mkdir`/`rm` and don't have shell-quoting hazards.

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `ReadDir(ctx, vmName, path)` | List directory entries with typed metadata. | `ctx`, `vmName` (string), `path` (string) | ([]SlicerFSInfo, error) |
| `Stat(ctx, vmName, path)` | Get a single file/directory entry; returns `os.ErrNotExist` on 404. | `ctx`, `vmName` (string), `path` (string) | (*SlicerFSInfo, error) |
| `Exists(ctx, vmName, path)` | Convenience wrapper around `Stat`. | `ctx`, `vmName` (string), `path` (string) | (bool, error) |
| `Mkdir(ctx, vmName, request)` | Create a directory (`recursive` + `mode` optional). | `ctx`, `vmName` (string), `request` (SlicerFSMkdirRequest) | error |
| `Remove(ctx, vmName, path, recursive)` | Remove a file or directory. | `ctx`, `vmName` (string), `path` (string), `recursive` (bool) | error |
| `ReadFile(ctx, vmName, vmPath)` | Download a single file's bytes plus its mode. | `ctx`, `vmName` (string), `vmPath` (string) | ([]byte, string, error) |
| `WriteFile(ctx, vmName, vmPath, data, uid, gid, permissions)` | Upload a single file with a specific mode. | `ctx`, `vmName` (string), `vmPath` (string), `data` ([]byte), `uid`/`gid` (uint32), `permissions` (string) | error |

#### Secret Management

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `CreateSecret(ctx, request)` | Create a new secret for VMs to use. Returns ErrSecretExists if a secret with the same name already exists. | `ctx` (context.Context), `request` (CreateSecretRequest) | error |
| `ListSecrets(ctx)` | List all secrets (metadata only, not values for security reasons) | `ctx` (context.Context) | ([]Secret, error) |
| `PatchSecret(ctx, secretName, request)` | Update an existing secret with new data and/or metadata. Only provided fields are modified. | `ctx` (context.Context), `secretName` (string), `request` (UpdateSecretRequest) | error |
| `DeleteSecret(ctx, secretName)` | Delete a secret | `ctx` (context.Context), `secretName` (string) | error |

### Documentation

- **Tutorial**: [Execute Commands in VM via SDK](https://docs.slicervm.com/tasks/execute-commands-with-sdk/)
- **Slicer API Reference**: [API](https://docs.slicervm.com/reference/api/)

### Samples/Examples

* [Create a VM with Userdata](examples/create/main.go)
* [Create a VM with k3s installed via Userdata](examples/k3s-userdata/main.go)s
* [Create a VM with claude code and run a headless inference](examples/claude/main.go)
* [Upload a file to process in VM, download the results](examples/transform/main.go)
* [Measure time to first interactive exec](examples/time_till_interactive/main.go)

### E2E benchmark notes (unix socket + devmapper + --min)

Use this for local benchmark/e2e runs on a dedicated daemon:

```bash
SLICER_BIN=${SLICER_BIN:-./path/to/slicer/bin/slicer}

cd /tmp
mkdir -p /tmp/sdk-e2e
$SLICER_BIN new timetti \
  --min \
  --count=0 \
  --storage=devmapper \
  --net=isolated \
  --api-bind /tmp/slicer-e2e.sock \
  > /tmp/sdk-e2e/slicer.yaml
$SLICER_BIN up /tmp/sdk-e2e/slicer.yaml > /tmp/sdk-e2e/slicer.log 2>&1 &
```

Then run the example:

```bash
SLICER_URL=/tmp/slicer-e2e.sock \
SLICER_HOST_GROUP=timetti \
SLICER_CREATE_WAIT=agent \
SLICER_CREATE_TIMEOUT=120s \
go run ./examples/time_till_interactive
```

Cleanup:

```bash
sudo kill -INT $(pgrep -f "/bin/slicer up /tmp/sdk-e2e/slicer.yaml")
```

### Quick start

Create a new slicer config with a `count` of `0`.

Every VM launched by API will be ephemeral, so it'll be deleted after running `sudo -E slicer vm shutdown NAME`, or when it's deleted either by an API call, this slicer daemon shutting down, or when you delete the VM via REST API.

The first launch will take the name `api-1`, then `api-2` and so forth. You can attach tags when you create the VM, if you want a stable name that can be looked up via the list endpoint.

```bash
slicer new api \
    --count=0 \
    --graceful-shutdown=false \
    --ram 4 \
    --cpu 2 > api.yaml
```

Start Slicer:

```bash
sudo -E slicer up ./api.yaml
```

Create a VM (node) in a host group with the default RAM/CPU settings as defined in the host group.

```go
package main

import (
    "fmt"
    "os"
    "context"
    
    sdk "github.com/slicervm/sdk"
)

func main() {
    // Typically you'd load these from environment variables
    baseURL := os.Getenv("SLICER_URL")      // API base URL
    token := os.Getenv("SLICER_TOKEN")      // Your API token
    userAgent := "my-microvm-client/1.0"
    hostGroup := "api"                       // Existing host group name

    client := sdk.NewSlicerClient(baseURL, token, userAgent, nil /* or &http.Client{} */)

    createReq := sdk.SlicerCreateNodeRequest{
        RamBytes:      4 * 1024 * 1024 * 1024, // 4GB RAM 
        CPUs:       2,
        Userdata: `#!/bin/bash
echo 'Bootstrapping...'
ping -c3 google.com

sudo reboot
`,
        SSHKeys: []string{"ssh-rsa AAAA..."}, // Optional: inject public SSH keys
        ImportUser: "alexellis", // Optional: Import GitHub keys for a specific user
    }

    ctx := context.Background()
    node, err := client.CreateVM(ctx, hostGroup, createReq)
    if err != nil {
        panic(fmt.Errorf("failed to create node: %w", err))
    }

    fmt.Printf("Created VM: hostname=%s ip=%s created_at=%s\n", node.Hostname, node.IP, node.CreatedAt)
    fmt.Printf("Parsed IP only: %s\n", node.IPAddress())
}
```

Run the program i.e. after running `go build -o client main.go`:

```bash
SLICER_URL=http://127.0.0.1:8080 SLICER_TOKEN="$(sudo cat /var/lib/slicer/auth/token)" ./client
```

You'll find the logs for the microVM at `/var/log/slicer/HOSTNAME.txt`, showing the userdata executing.

Notes:

* The argument order for `NewSlicerClient` is `(baseURL, token, userAgent, httpClient)`.
* If `RamBytes` or `CPUs` are not the values configured on the host group are used; `Userdata`, `SSHKeys` and `ImportUser` are optional.
* `Userdata` runs on first boot; keep it idempotent.
* Use a persistent `http.Client` (e.g. with timeout) in production instead of `nil`.

See a more minimal example at: [examples/create/main.go](examples/create/main.go)
