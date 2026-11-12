package nm

import (
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestSecurity(t *testing.T) {
	for _, c := range []struct {
		flags, wpa, rsn uint32
		want            string
	}{
		{0, 0, 0, "open"},
		{apFlagPrivacy, 0, 0, "wep"},
		{apFlagPrivacy, 0, secKeyMgmtPSK, "wpa"},
		{apFlagPrivacy, secKeyMgmtPSK, 0, "wpa"},
		{apFlagPrivacy, 0, secKeyMgmtPSK | secKeyMgmtSAE, "wpa"}, // WPA2/WPA3 transition
		{apFlagPrivacy, 0, secKeyMgmtSAE, "wpa3"},
		{apFlagPrivacy, 0, secKeyMgmt8021X, "enterprise"},
		{0, 0, secKeyMgmtOWE, "open"},
	} {
		if got := security(c.flags, c.wpa, c.rsn); got != c.want {
			t.Errorf("%#x/%#x/%#x = %s, want %s", c.flags, c.wpa, c.rsn, got, c.want)
		}
	}
	if keyMgmt("wpa3") != "sae" || keyMgmt("wpa") != "wpa-psk" {
		t.Error("key-mgmt")
	}
}

func TestNetworks(t *testing.T) {
	aps := []apInfo{
		{path: "/a", ssid: "Lyman", strength: 40, freq: 2437, rsn: secKeyMgmtPSK},
		{path: "/b", ssid: "Lyman", strength: 80, freq: 5180, rsn: secKeyMgmtPSK},
		{path: "/c", ssid: "", strength: 90, freq: 2412},
		{path: "/d", ssid: "Cafe", strength: 95, freq: 2412},
		{path: "/e", ssid: "Office", strength: 30, freq: 6115, rsn: secKeyMgmt8021X},
	}
	got := networks(aps, map[string]bool{"Lyman": true}, "Office")
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].SSID != "Office" || !got[0].Active || got[0].Supported() || got[0].Band() != "6 GHz" {
		t.Errorf("active first: %+v", got[0])
	}
	if got[1].SSID != "Cafe" || got[1].NeedsPassword() || !got[1].Supported() {
		t.Errorf("cafe: %+v", got[1])
	}
	if l := got[2]; l.SSID != "Lyman" || l.Signal != 80 || l.ap != "/b" || l.Band() != "5 GHz" || !l.Saved || !l.NeedsPassword() {
		t.Errorf("strongest AP kept: %+v", l)
	}
	if (Network{FreqMHz: 2462}).Band() != "2.4 GHz" || (Network{}).Band() != "" {
		t.Error("band")
	}
}

func TestWifiSettings(t *testing.T) {
	s := wifiSettings("Lyman", "wpa", "correct horse", false)
	if string(s["802-11-wireless"]["ssid"].Value().([]byte)) != "Lyman" ||
		s["802-11-wireless-security"]["key-mgmt"].Value() != "wpa-psk" ||
		s["802-11-wireless-security"]["psk"].Value() != "correct horse" ||
		s["ipv4"]["route-metric"].Value() != int64(PreferredMetric) {
		t.Errorf("%v", s)
	}
	if _, ok := s["802-11-wireless"]["hidden"]; ok {
		t.Error("hidden set")
	}
	open := wifiSettings("Cafe", "open", "", true)
	if _, ok := open["802-11-wireless-security"]; ok {
		t.Error("open network has security")
	}
	if open["802-11-wireless"]["hidden"] != dbus.MakeVariant(true) {
		t.Error("hidden not set")
	}
	if Reason(7) != "the password is wrong or missing" || Reason(999) != "NetworkManager reason 999" {
		t.Error("reason")
	}
	if _, err := Trim("  "); err == nil {
		t.Error("empty ssid")
	}
	if s, err := Trim(" Lyman "); s != "Lyman" || err != nil {
		t.Error("trim")
	}
}
