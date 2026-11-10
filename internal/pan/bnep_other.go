//go:build !linux

package pan

// Conns is Linux only.
func Conns() ([]Conn, error) { return nil, ErrUnsupported }

// Kill is Linux only.
func Kill(string) error { return ErrUnsupported }
