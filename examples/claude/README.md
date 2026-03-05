# claude example

This example provisions a VM, installs Claude CLI via `arkade`, copies authentication/config files, and runs a text-mode command.

What it does:

- Reads `SLICER_URL` and `SLICER_TOKEN` from the environment
- Creates a VM and waits for the slicer agent to become healthy
- Extracts credentials from macOS keychain item `Claude Code-credentials`:
  - `security find-generic-password -s "Claude Code-credentials" -w`
- Copies into VM:
  - `/home/ubuntu/.claude/.credentials.json` (required)
  - `/home/ubuntu/.claude/settings.json` (optional if present locally)
  - `/home/ubuntu/.claude.json` (optional, fallback default is generated)
- Installs Claude CLI in cloud-init userdata via `arkade`
- Runs `claude -p "What's my username?"` as UID/GID `1000`

Run:

```bash
SLICER_URL=http://192.168.1.34:8080 \
SLICER_TOKEN=$(cat ~/slicer-token.txt) \
go run ./examples/claude
```

Optional overrides:

- `SLICER_HOST_GROUP` (default: `vm`)
- `SLICER_CLAUDE_TIMEOUT` (defaults in code via context budget)
