# Run Claude in a Slicer VM

This example uses the Go SDK to create a VM, install Claude Code, and copy the
caller's Claude credentials. It can run one headless prompt in a disposable VM
or start a persistent background session for later attachment. It works with
both Slicer on Linux and Slicer for Mac because it uses the shared HTTP API and
supports TCP and Unix-socket transports.

It deliberately demonstrates the SDK primitives rather than reproducing the
full `slicer claude` command. The CLI also provides cached and verified agent
downloads, workspace and worktree transfer, safe Git configuration, friendly
VM names, interactive and tmux sessions, reattachment, and setup rollback.

## Run with Slicer on Linux

For a TCP API with authentication enabled:

```bash
SLICER_URL=https://slicer.example.com \
SLICER_TOKEN_FILE=/var/lib/slicer/auth/token \
go run . --prompt "Review the code in /home/ubuntu and summarise it"
```

For an unauthenticated Unix socket, omit the token:

```bash
SLICER_URL=/path/to/slicer.sock \
go run . --prompt "What kernel version is this VM running?"
```

## Run with Slicer for Mac

Point the example at the Slicer for Mac socket. When the daemon has both
`slicer` and `sbox` host groups, the example selects the disposable `sbox`
group automatically:

```bash
SLICER_URL=~/slicer-mac/slicer.sock \
go run . --prompt "What architecture is this VM using?"
```

Use `--hostgroup` or `SLICER_HOST_GROUP` to override host-group selection.

## Claude credentials

The example looks for credentials in this order:

1. `--credentials-file` or `CLAUDE_CREDENTIALS_FILE`
2. `~/.claude/.credentials.json`
3. the `Claude Code-credentials` item in the macOS Keychain

It copies the credential into the VM as
`/home/ubuntu/.claude/.credentials.json`, mode `0600`. The VM is deleted after
Claude exits, including when setup or inference fails. Use `--keep` only when
you deliberately want to retain the VM and its copied credential.

Claude runs in `/home/ubuntu/workspace`. The example creates that directory
and records trust for that exact path so a non-interactive or background start
does not block on Claude's workspace trust dialogue. A platform can copy or
clone its own workload into that directory before invoking Claude.

The host's `~/.claude.json` and `~/.claude/settings.json` are not copied by
default. They can contain unrelated project history, hooks, and machine-local
configuration. Pass `--settings-file` or `CLAUDE_SETTINGS_FILE` when a known,
reviewed settings file is required inside the VM.

## Prompt and session behaviour

`--prompt` performs one new, headless Claude invocation. It is not written to
`run.sh`, stored in the VM, or replayed if the VM or daemon is restarted.
Running the example again creates a fresh VM and a fresh Claude conversation.

This separation is intentional: an interactive bootstrap script and a queued
task have different lifecycle and retry semantics. A service that needs
durable tasks or session continuation should store that state in its own
control plane and invoke the SDK explicitly.

To start work now and attach to the same Claude session later, use Claude's
background-session support:

```bash
SLICER_URL=/path/to/slicer.sock \
go run . --background --prompt "Review issue #123 and start reproducing it"
```

The example creates a persistent VM in this mode and prints Claude's background
session identifier. Open a shell into the VM later, then use `claude agents`
and `claude attach <id>`. The prompt is submitted once by Claude; it is not
replayed by Slicer when the VM is relaunched.

An interactive implementation could instead put Claude in a named tmux session.
In that design the tmux session is the state: the first invocation creates the
session with the initial prompt, while subsequent invocations attach to the
existing session. No separate lock file is required.

Useful options:

```text
--prompt string            Prompt for the one-shot Claude invocation
--background               Start a Claude session for later attachment
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
