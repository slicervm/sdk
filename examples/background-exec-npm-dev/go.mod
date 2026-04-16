module github.com/slicervm/sdk/examples/background-exec-npm-dev

go 1.24.0

require github.com/slicervm/sdk v0.0.44

require github.com/coder/websocket v1.8.14 // indirect

// Local SDK - background-exec APIs not yet released.
replace github.com/slicervm/sdk => ../..
