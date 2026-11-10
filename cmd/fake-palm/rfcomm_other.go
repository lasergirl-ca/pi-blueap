//go:build !linux

package main

import (
	"errors"
	"os"
)

func dialRFCOMM(_, _ string, _ int) (*os.File, error) {
	return nil, errors.New("RFCOMM sockets need Linux")
}
