package allow

import (
	"testing"

	"palm-gw/internal/state"
)

func TestAllowlist(t *testing.T) {
	st, err := state.Open(state.Path(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	l := New([]string{"00:07:e0:aa:bb:cc"}, st, false)

	if ok, src := l.Check("00:07:E0:AA:BB:CC"); !ok || src != SourceStatic {
		t.Fatalf("static entry: %v %v", ok, src)
	}
	if l.Allowed("11:22:33:44:55:66") {
		t.Fatal("unknown device must be refused")
	}
	if err := l.Add("11:22:33:44:55:66", "Palm"); err != nil {
		t.Fatal(err)
	}
	if ok, src := l.Check("11:22:33:44:55:66"); !ok || src != SourceDynamic {
		t.Fatalf("dynamic entry: %v %v", ok, src)
	}
	e := l.Entries()
	if len(e) != 2 || e[0].Source != SourceStatic || e[1].Name != "Palm" {
		t.Fatalf("entries: %+v", e)
	}

	removed, static, err := l.Remove("11:22:33:44:55:66")
	if err != nil || !removed || static {
		t.Fatalf("remove dynamic: %v %v %v", removed, static, err)
	}
	removed, static, err = l.Remove("00:07:E0:AA:BB:CC")
	if err != nil || removed || !static {
		t.Fatalf("remove static must report static: %v %v %v", removed, static, err)
	}
	if !l.Allowed("00:07:E0:AA:BB:CC") {
		t.Fatal("static entries survive Remove")
	}

	l.Reconfigure(nil, true)
	if ok, src := l.Check("de:ad:be:ef:00:01"); !ok || src != SourceAny {
		t.Fatalf("allow_any: %v %v", ok, src)
	}
	if l.Allowed("00:07:E0:AA:BB:CC") && len(l.Entries()) != 0 {
		t.Fatal("Reconfigure must drop old static entries")
	}
}
