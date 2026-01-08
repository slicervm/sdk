## Golang SDK for Slicer

SDK for [SlicerVM.com](https://slicervm.com)

### Installation

```bash
go get github.com/slicervm/sdk@latest
```

### Features

- **VM Management**: Create, list, and delete VMs.
- **Execute Commands in VMs**: Run commands in VMs and stream command output (stdout/stderr).
- **File Management**: Upload and download files to/from VMs with `CpToVM` and `CpFromVM`.
- **Secret Management**: Securely manage secrets like API keys or other sensitive information for VMs.

### Documentation

- **Tutorial**: [Execute Commands in VM via SDK](https://docs.slicervm.com/tasks/execute-commands-with-sdk/)
- **Slicer API Reference**: [API](https://docs.slicervm.com/reference/api/)

### Quick start

Create a new slicer config:

```bash
slicer new api \
    --count=0 \
    --graceful-shutdown=false \
    --ram 4 \
    --cpu 2 > api.yaml
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
    node, err := client.CreateNode(ctx, hostGroup, createReq)
    if err != nil {
        panic(fmt.Errorf("failed to create node: %w", err))
    }

    fmt.Printf("Created VM: hostname=%s ip=%s created_at=%s\n", node.Hostname, node.IP, .CreatedAt)
    fmt.Printf("Parsed IP only: %s\n", node.IPAddress())
}
```

Start Slicer:

```bash
sudo -E slicer up ./api.yaml
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
