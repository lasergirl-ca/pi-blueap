package pan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseLeases(t *testing.T) {
	in := `1731300000 60:15:6f:e9:eb:03 10.42.0.100 fakepalm 01:60:15:6f:e9:eb:03
0 aa:bb:cc:dd:ee:ff 10.42.0.101 * *
duid 00:01:00:01:2c:aa:bb:cc
garbage
`
	got, err := ParseLeases(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []Lease{
		{Expires: time.Unix(1731300000, 0), MAC: "60:15:6F:E9:EB:03", IP: "10.42.0.100", Hostname: "fakepalm"},
		{MAC: "AA:BB:CC:DD:EE:FF", IP: "10.42.0.101"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestReadLeasesMissingFile(t *testing.T) {
	got, err := ReadLeases(filepath.Join(t.TempDir(), "none"))
	if err != nil || got != nil {
		t.Errorf("got %v, %v; want nil, nil", got, err)
	}
}

func TestBridgeAndPorts(t *testing.T) {
	dir := t.TempDir()
	old := SysClassNet
	SysClassNet = dir
	t.Cleanup(func() { SysClassNet = old })
	if BridgePresent("br-palm") {
		t.Fatal("bridge present in an empty directory")
	}
	if err := os.MkdirAll(filepath.Join(dir, "br-palm", "brif", "bnep0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "br-palm", "brif", "bnep1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !BridgePresent("br-palm") {
		t.Fatal("bridge not found")
	}
	if got := Ports("br-palm"); !reflect.DeepEqual(got, []string{"bnep0", "bnep1"}) {
		t.Errorf("ports %v", got)
	}
}
