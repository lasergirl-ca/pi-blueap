package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"palm-gw/internal/config"
	"palm-gw/internal/ctl"
	"palm-gw/internal/fw"
)

func TestHandleNAT(t *testing.T) {
	a := netip.MustParseAddr
	conns := []fw.Conn{
		{Proto: "tcp", Src: a("10.4.0.54"), Dst: a("10.4.1.118"), DstPort: 8042},
		{Proto: "tcp", Src: a("10.42.0.10"), Dst: a("93.184.215.14"), DstPort: 80, NATAddr: a("10.4.1.118"), NATPort: 1028},
	}
	var tableErr error
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := &Daemon{log: log, opts: Options{Conntrack: func() ([]fw.Conn, error) { return conns, tableErr }}}
	d.cfgp.Store(config.Default())
	srv := ctl.NewServer("", "", log)
	srv.Handle("nat", d.handleNAT)
	call := func() (ctl.NATReply, error) {
		var r ctl.NATReply
		err := srv.Call(context.Background(), ctl.Request{Cmd: "nat"}, func(line json.RawMessage) error {
			var e ctl.ErrorReply
			if json.Unmarshal(line, &e) == nil && e.Error != "" {
				return errors.New(e.Error)
			}
			return json.Unmarshal(line, &r)
		})
		return r, err
	}
	r, err := call()
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || len(r.Entries) != 1 || r.Entries[0].NATPort != 1028 || r.Subnet != "10.42.0.0/24" {
		t.Errorf("%+v", r)
	}
	tableErr = errors.New("conntrack: operation not permitted")
	if _, err := call(); err == nil {
		t.Error("table error not passed on")
	}
}
