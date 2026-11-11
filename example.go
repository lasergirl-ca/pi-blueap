// Package palmgw holds files that live at the repository root but are needed
// inside the binaries: the example configuration, which `palm-gw setup`
// starts from when there is no config.toml yet, so a new one keeps every
// comment.
package palmgw

import _ "embed"

// ExampleConfig is config.example.toml.
//
//go:embed config.example.toml
var ExampleConfig string
