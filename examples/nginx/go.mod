module github.com/slicervm/sdk/examples/nginx

go 1.24.0

require github.com/slicervm/sdk v0.0.42

require github.com/coder/websocket v1.8.14 // indirect

// Local SDK includes the new forward subpackage (not yet released).
replace github.com/slicervm/sdk => ../..
