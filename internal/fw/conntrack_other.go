//go:build !linux

package fw

import "errors"

// Conntrack is Linux only.
func Conntrack() ([]Conn, error) { return nil, errors.New("conntrack: Linux only") }
