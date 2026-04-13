# box example

This example targets the Box environment and demonstrates a minimal workflow:

- Connect using `SLICER_URL`, `SLICER_TOKEN`, and/or `SLICER_TOKEN_FILE`
- List VMs with `client.ListVMs`
- Select `vms[0]`
- Run `uname -a` with `client.Exec`

Run:

```bash
export SLICER_URL=https://box.slicervm.com
export SLICER_TOKEN_FILE=~/.slicer/gh-access-token

go run .
```

Run from this directory. The example module requires `github.com/slicervm/sdk v0.0.42` and uses `replace github.com/slicervm/sdk => ../../` so local SDK changes are used.
