package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palm-gw/internal/config"
)

// A trimmed copy of Debian's stock /etc/bluetooth/main.conf.
const stock = `[General]

# Default adapter name
# Defaults to 'BlueZ X.YZ'
#Name = BlueZ

# Default device class. Only the major and minor device class bits are
# considered. Defaults to '0x000000'.
#Class = 0x000100

# How long to stay in discoverable mode before going back to non-discoverable
#DiscoverableTimeout = 0

#JustWorksRepairing = never

[BR]
# The following values are used to load default adapter parameters for BR/EDR.
#PageScanType=

[Policy]
#
# The ReconnectUUIDs defines the set of remote services that should try to be
#AutoEnable=true
`

func settings(t *testing.T) []Setting {
	t.Helper()
	cfg := config.Default()
	cfg.Bluetooth.Name = "Palm Gateway"
	return ManagedSettings(cfg, cfg.Bluetooth.Class)
}

func TestApplyStock(t *testing.T) {
	out, changed := Apply(stock, settings(t))
	if !changed {
		t.Fatal("expected change")
	}
	// Block directly after [General], before the stock comments.
	if !strings.Contains(out, "[General]\n"+BeginMarker+"\nName = Palm Gateway\nClass = 0x000300\nDiscoverableTimeout = 0\nPairableTimeout = 0\nJustWorksRepairing = always\nControllerMode = dual\nReverseServiceDiscovery = false\n"+EndMarker+"\n") {
		t.Fatalf("General block wrong:\n%s", out)
	}
	if !strings.Contains(out, "[Policy]\n"+BeginMarker+"\nAutoEnable = true\n"+EndMarker+"\n") {
		t.Fatalf("Policy block wrong:\n%s", out)
	}
	// Commented stock keys are left alone; [BR] untouched.
	if !strings.Contains(out, "#Name = BlueZ") || !strings.Contains(out, "#PageScanType=") || strings.Contains(out, DisabledPrefix) {
		t.Fatalf("stock lines altered:\n%s", out)
	}
	// Idempotent.
	again, changed2 := Apply(out, settings(t))
	if changed2 || again != out {
		t.Fatalf("not idempotent:\n%s", again)
	}
	// Round trip.
	back, _ := Remove(out)
	if back != stock {
		t.Fatalf("Remove did not restore the original:\n%s", back)
	}
}

func TestApplyParksLiveKeysAndRestores(t *testing.T) {
	in := "[General]\nName = My Pi\nClass = 0x000100\nSomethingElse = 1\n\n[Policy]\nAutoEnable=false\n"
	out, _ := Apply(in, settings(t))
	for _, want := range []string{
		DisabledPrefix + "Name = My Pi",
		DisabledPrefix + "Class = 0x000100",
		DisabledPrefix + "AutoEnable=false",
		"SomethingElse = 1",
		"Name = Palm Gateway",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	// Only one live Name remains.
	if strings.Count(out, "\nName = ") != 1 {
		t.Fatalf("duplicate live Name:\n%s", out)
	}
	back, _ := Remove(out)
	if back != in {
		t.Fatalf("round trip failed:\n%s", back)
	}
}

func TestApplyAddsMissingSections(t *testing.T) {
	out, _ := Apply("[BR]\n#x\n", settings(t))
	if !strings.Contains(out, "\n[General]\n"+BeginMarker) || !strings.Contains(out, "\n[Policy]\n"+BeginMarker) {
		t.Fatalf("sections not appended:\n%s", out)
	}
	out2, _ := Apply("", settings(t))
	if !strings.HasPrefix(out2, "[General]\n"+BeginMarker) {
		t.Fatalf("empty file:\n%s", out2)
	}
}

func TestApplyChangesValueOnReapply(t *testing.T) {
	cfg := config.Default()
	out, _ := Apply(stock, ManagedSettings(cfg, 0x000300))
	out2, changed := Apply(out, ManagedSettings(cfg, 0x000104))
	if !changed || !strings.Contains(out2, "Class = 0x000104") || strings.Contains(out2, "Class = 0x000300") {
		t.Fatalf("class toggle not applied:\n%s", out2)
	}
}

func TestApplyFileRemoveFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "main.conf")
	if err := os.WriteFile(p, []byte(stock), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := ApplyFile(p, settings(t))
	if err != nil || !changed {
		t.Fatalf("apply: %v %v", changed, err)
	}
	changed, err = ApplyFile(p, settings(t))
	if err != nil || changed {
		t.Fatalf("second apply must be a no-op: %v %v", changed, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
	changed, err = RemoveFile(p)
	if err != nil || !changed {
		t.Fatalf("remove: %v %v", changed, err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != stock {
		t.Fatalf("file not restored:\n%s", data)
	}
	if _, err := os.Stat(p + ".palm-gw.tmp"); err == nil {
		t.Fatal("temp file left behind")
	}
}
