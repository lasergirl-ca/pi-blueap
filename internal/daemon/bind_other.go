//go:build !linux

package daemon

import "errors"

// bindToDevice is Linux only; elsewhere (tests on a Mac) a check is refused.
func bindToDevice(int, string) error { return errors.New("SO_BINDTODEVICE is Linux only") }
