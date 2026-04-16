module github.com/slicervm/sdk/examples/watch-make-arkade-bin

go 1.24.0

require (
	github.com/google/uuid v1.6.0
	github.com/slicervm/sdk v0.0.42
)

// Local SDK includes the fs/watch API (not yet released).
replace github.com/slicervm/sdk => ../..
