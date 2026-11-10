package ppp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProcRoot is where the orphan tripwire looks; tests point it elsewhere.
var ProcRoot = "/proc"

// Orphan is a pppd the session table does not account for.
type Orphan struct {
	PID  int
	PGID int
	Cmd  string
}

// Orphans finds pppd processes started by this daemon (their command line
// carries `linkname palm-`) whose process group is not a live session. A
// test client's pppd on the same host has no such linkname and is ignored.
func (m *Manager) Orphans() []Orphan {
	known := m.knownPGIDs()
	ents, err := os.ReadDir(ProcRoot)
	if err != nil {
		return nil
	}
	var out []Orphan
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(ProcRoot, e.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != "pppd" {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || !bytes.Contains(cmdline, []byte("linkname\x00palm-")) {
			continue
		}
		pgid := statPGID(filepath.Join(dir, "stat"))
		if pgid != 0 && known[pgid] {
			continue
		}
		out = append(out, Orphan{PID: pid, PGID: pgid, Cmd: strings.ReplaceAll(strings.TrimRight(string(cmdline), "\x00"), "\x00", " ")})
	}
	return out
}

// statPGID reads field 5 (pgrp) of /proc/<pid>/stat. The command name in
// field 2 may contain spaces, so parse after the last ')'.
func statPGID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(f[2])
	return n
}

// RunReaper logs orphan_pppd every interval until ctx ends (PLAN.md section
// 8.5: a tripwire for fd or process leaks; it should never fire).
func (m *Manager) RunReaper(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, o := range m.Orphans() {
			m.log.Error("pppd not in the session table", "event", "orphan_pppd", "pid", o.PID, "pgid", o.PGID, "cmd", o.Cmd)
			m.events.Emit("orphan_pppd", "", "", "pid", strconv.Itoa(o.PID))
		}
	}
}
