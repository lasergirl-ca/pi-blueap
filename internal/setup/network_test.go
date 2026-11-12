package setup

import (
	"path/filepath"
	"strings"
	"testing"

	"palm-gw/internal/config"
)

func TestNetworkEdits(t *testing.T) {
	cur := config.Default()
	on, off := true, false
	for _, c := range []struct {
		ch      NetworkChange
		want    string // section.key=value, space separated
		wantErr string
	}{
		{NetworkChange{}, "", ""},
		{NetworkChange{Uplink: "eth0", Subnet: "10.42.0.0/24", TLS: "self-signed", BluetoothSide: &on}, "", ""},
		{NetworkChange{Uplink: "wlan0"}, `network.uplink="wlan0"`, ""},
		{NetworkChange{Subnet: "10.43.0.0/24"}, `network.subnet="10.43.0.0/24" network.gateway="10.43.0.1" network.ppp_pool=["10.43.0.10", "10.43.0.99"] network.dhcp_pool=["10.43.0.100", "10.43.0.199"]`, ""},
		{NetworkChange{TLS: "off"}, `web.tls="off" web.bluetooth_side=false`, ""},
		{NetworkChange{BluetoothSide: &off}, `web.bluetooth_side=false`, ""},
		{NetworkChange{TLS: "off", BluetoothSide: &on}, "", "needs HTTPS"},
		{NetworkChange{TLS: "file"}, "", "want self-signed or off"},
		{NetworkChange{Uplink: "ppp0"}, "", "own interfaces"},
		{NetworkChange{Subnet: "10.43.0.1/24"}, "", "want an IPv4 /24"},
	} {
		edits, err := NetworkEdits(cur, c.ch)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%+v: err %v, want %q", c.ch, err, c.wantErr)
			}
			continue
		}
		var got []string
		for _, e := range edits {
			got = append(got, e.Section+"."+e.Key+"="+e.Value)
		}
		if strings.Join(got, " ") != c.want || err != nil {
			t.Errorf("%+v:\n got %s (%v)\nwant %s", c.ch, strings.Join(got, " "), err, c.want)
		}
	}
}

func TestLabels(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{Adapter{HCI: "hci0", Address: "60:15:6F:EA:00:81", Bus: "usb"}.Label(), "USB dongle 60:15:6F:EA:00:81 (hci0)"},
		{Adapter{HCI: "hci1", Address: "2C:CF:67:2F:29:F5", Bus: "uart"}.Label(), "Onboard radio 2C:CF:67:2F:29:F5 (hci1)"},
		{Uplink{Iface: "eth0", Gateway: "10.4.0.1"}.Label(), "Ethernet eth0, via router 10.4.0.1"},
		{Uplink{Iface: "wlan0", Gateway: "10.4.0.1", Wireless: true, FreqMHz: 2437}.Label(), "Wi-Fi wlan0 (2.4 GHz), via router 10.4.0.1"},
		{Uplink{Iface: "wlan0", Wireless: true}.Label(), "Wi-Fi wlan0"},
		{Uplink{Iface: "usb0"}.Label(), "USB network usb0"},
		{Uplink{Iface: "wwan0"}.Label(), "wwan0"},
	} {
		if c.got != c.want {
			t.Errorf("%q, want %q", c.got, c.want)
		}
	}
}

func TestApplyRecord(t *testing.T) {
	p := filepath.Join(t.TempDir(), "last-apply.json")
	if r, err := ReadApplyRecord(p); r != nil || err != nil {
		t.Fatalf("missing: %v %v", r, err)
	}
	if err := WriteApplyRecord(p, ApplyRecord{OK: true, Changed: []string{"/etc/palm-gw/dnsmasq.conf"}}); err != nil {
		t.Fatal(err)
	}
	if r, err := ReadApplyRecord(p); err != nil || !r.OK || len(r.Changed) != 1 {
		t.Errorf("%+v %v", r, err)
	}
}
