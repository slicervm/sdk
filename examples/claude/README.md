# Run Claude in a Slicer VM

This example uses the Go SDK to create a VM, install Claude Code, and copy the
caller's Claude credentials. It can run one headless prompt in a disposable VM
or start a persistent background session for later attachment. It works with
both Slicer on Linux and Slicer for Mac because it uses the shared HTTP API and
supports TCP and Unix-socket transports.

The guest is expected to use the standard Slicer Ubuntu layout: user and group
ID 1000, `/home/ubuntu`, `python3`, `install`, and `arkade` must be available.

It deliberately demonstrates the SDK primitives rather than reproducing the
full `slicer claude` command. The CLI also provides cached and verified agent
downloads, workspace and worktree transfer, safe Git configuration, friendly
VM names, interactive and tmux sessions, reattachment, and setup rollback.

By default, Claude keeps its normal permission policy. The commands below use
`--unrestricted` when the prompt needs shell or file tools. This passes
Claude's `--dangerously-skip-permissions` option. Use it only with prompts,
workspace contents, and settings you trust: Claude can run commands without a
confirmation prompt, read the copied credential, and use the VM's network.

## Run with Slicer on Linux

For a TCP API with authentication enabled:

```bash
SLICER_URL=https://slicer.example.com \
SLICER_TOKEN="$(sudo cat /var/lib/slicer/auth/token)" \
go run . --unrestricted \
  --prompt "Summarise the VM's CPU, memory, kernel, and operating system"
```

For an unauthenticated Unix socket, omit the token:

```bash
SLICER_URL=/path/to/slicer.sock \
go run . --unrestricted --prompt "What kernel version is this VM running?"
```

## Run with Slicer for Mac

Point the example at the Slicer for Mac socket. When the daemon has both
`slicer` and `sbox` host groups, the example selects the disposable `sbox`
group automatically:

```bash
SLICER_URL=~/slicer-mac/slicer.sock \
go run . --unrestricted --prompt "What architecture is this VM using?"
```

Use `--hostgroup` or `SLICER_HOST_GROUP` to override host-group selection.

## Claude credentials

The example looks for credentials in this order:

1. `--credentials-file` or `CLAUDE_CREDENTIALS_FILE`
2. `~/.claude/.credentials.json`
3. the `Claude Code-credentials` item in the macOS Keychain

It copies the credential into the VM as
`/home/ubuntu/.claude/.credentials.json`, mode `0600`. In one-shot mode, the VM
is deleted after Claude exits, including when later setup or inference fails.
`--keep` retains a one-shot VM. A successfully started `--background` session
retains a persistent VM, including the copied credential, until you delete it.
Failed setup or background startup is cleaned up automatically unless `--keep`
is set. More precisely, the example attempts deletion for up to 30 seconds and
exits non-zero if the API never confirms success or `404 Not Found`; verify that
failure manually using the printed hostname or `run=claude-...` tag. `--keep`
also retains failed VMs for inspection.

Claude runs in `/home/ubuntu/workspace`. The example creates that directory
and records trust for that exact path so a non-interactive or background start
does not block on Claude's workspace trust dialogue. A platform can copy or
clone its own workload into that directory before invoking Claude.

The host's `~/.claude.json` and `~/.claude/settings.json` are not copied by
default. They can contain unrelated project history, hooks, and machine-local
configuration. Pass `--settings-file` or `CLAUDE_SETTINGS_FILE` when a known,
reviewed settings file is required inside the VM.

## Prompt and session behaviour

`--prompt` performs one new, headless Claude invocation. The example does not
write it to `run.sh`, keep its own task record, or replay it if the VM or daemon
is restarted. Running the example again creates a fresh VM and a fresh Claude
conversation.

This separation is intentional: an interactive bootstrap script and a queued
task have different lifecycle and retry semantics. A service that needs
durable tasks or session continuation should store that state in its own
control plane and invoke the SDK explicitly.

To start work now and attach to the same Claude session later, use Claude's
background-session support:

```bash
printf '%s\n' 'Review issue #123 and start reproducing it' | \
  go run . --url /path/to/slicer.sock --background --unrestricted \
    --prompt-file -
```

`--prompt-file -` keeps sensitive prompt text out of shell history and the
local process list. `--prompt` is convenient for non-sensitive text, but its
value remains visible locally even though the SDK sends it to Slicer over the
request body rather than in the request URL.

The example creates a persistent VM in this mode and prints Claude's background
session identifier and VM hostname. Assign the printed hostname, then open a
shell into the VM:

```bash
VM_NAME=e2e-2

# Authenticated TCP API
SLICER_TOKEN="$(sudo cat /var/lib/slicer/auth/token)" \
slicer vm shell "$VM_NAME" --url https://slicer.example.com

# Or Slicer for Mac / an unauthenticated Unix socket
slicer vm shell "$VM_NAME" --url ~/slicer-mac/slicer.sock
```

Inside the VM, run `claude agents`, followed by `claude attach <id>`. Claude
retains its own session and prompt state inside the VM so that logs and
attachment work. The prompt is submitted once; Slicer does not replay it when
the VM is relaunched.

The retained VM also contains the copied Claude credential. Delete it when the
session is no longer required:

```bash
VM_NAME=e2e-2

# Authenticated TCP API
SLICER_TOKEN="$(sudo cat /var/lib/slicer/auth/token)" \
slicer vm delete "$VM_NAME" --url https://slicer.example.com

# Slicer for Mac or an unauthenticated local Unix socket
slicer vm delete "$VM_NAME" --url ~/slicer-mac/slicer.sock
```

If automatic cleanup reports an error and no hostname was returned, use the
printed run tag to find any late-created VM before deleting it:

```bash
RUN_TAG=claude-0123456789ab

# Authenticated TCP API
SLICER_TOKEN="$(sudo cat /var/lib/slicer/auth/token)" \
slicer vm list --json --url https://slicer.example.com | \
  jq --arg tag "run=$RUN_TAG" '.[] | select(.tags | index($tag))'

# Or Slicer for Mac / an unauthenticated Unix socket
slicer vm list --json --url ~/slicer-mac/slicer.sock | \
  jq --arg tag "run=$RUN_TAG" '.[] | select(.tags | index($tag))'
```

An interactive implementation could instead put Claude in a named tmux session.
In that design the tmux session is the state: the first invocation creates the
session with the initial prompt, while subsequent invocations attach to the
existing session. No separate lock file is required.

Useful options:

```text
--prompt string            Prompt to send to Claude
--prompt-file string       Read prompt from a file, or - for stdin
--background               Start a Claude session for later attachment
--unrestricted             Allow tools without permission prompts
--hostgroup string         Explicit host group
--credentials-file string  Claude credentials file
--settings-file string     Optional reviewed Claude settings file
--token-file string        Slicer API token file
--cpus int                 VM CPU count (default 2)
--ram-gb int               VM RAM in GiB (default 4)
--timeout duration         Overall timeout (default 10m)
--keep                     Retain the VM after Claude exits
```

The example module uses the local SDK checkout through its `replace` directive,
so run it from this directory:

```bash
go run . --help
```
