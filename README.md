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

#### VM Operations

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `CreateVM(ctx, groupName, request)` | Create a new VM in a host group | `ctx` (context.Context), `groupName` (string), `request` (SlicerCreateNodeRequest) | (*SlicerCreateNodeResponse, error) |
| `DeleteVM(ctx, groupName, hostname)` | Delete a VM from a host group | `ctx` (context.Context), `groupName` (string), `hostname` (string) | (*SlicerDeleteResponse, error) |
| `ListVMs(ctx)` | List all VMs across all host groups | `ctx` (context.Context) | ([]SlicerNode, error) |
| `GetHostGroups(ctx)` | Fetch all host groups | `ctx` (context.Context) | ([]SlicerHostGroup, error) |
| `GetHostGroupNodes(ctx, groupName)` | Fetch nodes for a specific host group | `ctx` (context.Context), `groupName` (string) | ([]SlicerNode, error) |
| `DeleteNode(groupName, nodeName)` | Delete a node from a host group | `groupName` (string), `nodeName` (string) | error |
| `PauseVM(ctx, hostname)` | Pause a running VM to save CPU cost | `ctx` (context.Context), `hostname` (string) | error |
| `ResumeVM(ctx, hostname)` | Resume a paused VM | `ctx` (context.Context), `hostname` (string) | error |
| `Shutdown(ctx, hostname, request)` | Shutdown or reboot a VM | `ctx` (context.Context), `hostname` (string), `request` (*SlicerShutdownRequest) | error |
| `GetVMStats(ctx, hostname)` | Get CPU, memory, and disk statistics for a VM or all VMs | `ctx` (context.Context), `hostname` (string, empty for all) | ([]SlicerNodeStat, error) |
| `GetVMLogs(ctx, hostname, lines)` | Get recent logs from a VM | `ctx` (context.Context), `hostname` (string), `lines` (int, -1 for all) | (*SlicerLogsResponse, error) |
| `GetInfo(ctx)` | Fetch server version and build information | `ctx` (context.Context) | (*SlicerInfo, error) |

#### Guest Operations

| Method | Description | Parameters | Returns |
|--------|-------------|------------|---------|
| `Exec(ctx, hostname, request)` | Execute a command in a VM and stream output line-by-line | `ctx` (context.Context), `hostname` (string), `request` (SlicerExecRequest) | (chan SlicerExecWriteResult, error) |
| `CpToVM(ctx, vmName, localPath, vmPath, uid, gid, permissions, mode)` | Upload a file/directory to a VM | `ctx` (context.Context), `vmName` (string), `localPath` (string), `vmPath` (string), `uid` (uint32), `gid` (uint32), `permissions` (string), `mode` (string: "tar" or "binary") | error |
| `CpFromVM(ctx, vmName, vmPath, localPath, permissions, mode)` | Download a file/directory from a VM | `ctx` (context.Context), `vmName` (string), `vmPath` (string), `localPath` (string), `permissions` (string), `mode` (string: "tar" or "binary") | error |
| `GetAgentHealth(ctx, hostname, includeStats)` | Check VM agent health and optionally get system stats | `ctx` (context.Context), `hostname` (string), `includeStats` (bool) | (*SlicerAgentHealthResponse, error) |

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

    fmt.Printf("Created VM: hostname=%s ip=%s created_at=%s\n", node.Hostname, node.IP, .CreatedAt)
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
