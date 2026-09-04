# egress-filter

Proves the Slicer egress-proxy allow/deny/secret rules. A single Go process
supervises a real Slicer daemon and egress proxy, configures them via the SDK,
then asserts on traffic from inside an isolated microVM.

## What it proves

1. **Allowed + secret injected** — an allowed upstream returns 200 and sees the
   proxy-injected credential (`Bearer banshee`), so real credentials never enter
   the guest.
2. **Unknown refused** — an upstream with no allow rule is denied (default deny).
3. **Revoked refused** — after `RemoveProxyAllow` the allowed upstream is denied
   again.

## Run

Requires a Slicer host (Firecracker + licence), e.g. in `tmux`. Run it from the
SDK repo:

```bash
go run ./examples/egress-filter --bin /usr/local/bin/slicer \
  --license-file /home/alex/.slicer/LICENSE
```

The binary boots the daemon and proxy as children it owns, configures the proxy
via `CreateProxyClient` / `CreateProxySecret` / `AddProxyAllow`, runs the VM
checks, then tears the whole stack down.

`-keep` leaves the daemon up on `0.0.0.0:8080` for inspection; `-storage`,
`-group` and `-api-port` are the main knobs.
