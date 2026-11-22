package mm

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestParse(t *testing.T) {
	if _, err := Parse(nil); !errors.Is(err, ErrNoModem) {
		t.Fatalf("empty: %v", err)
	}
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		"/org/freedesktop/ModemManager1/SIM/0": {"org.freedesktop.ModemManager1.Sim": {}},
		"/org/freedesktop/ModemManager1/Modem/0": {
			"org.freedesktop.ModemManager1.Modem": {
				"Manufacturer":       dbus.MakeVariant("SIMCOM INCORPORATED"),
				"Model":              dbus.MakeVariant("SIMCOM_SIM7600G-H"),
				"State":              dbus.MakeVariant(int32(11)),
				"UnlockRequired":     dbus.MakeVariant(uint32(1)),
				"SignalQuality":      dbus.MakeVariant([]any{uint32(72), true}),
				"AccessTechnologies": dbus.MakeVariant(uint32(1 << 14)),
			},
			"org.freedesktop.ModemManager1.Modem.Modem3gpp": {
				"OperatorName":      dbus.MakeVariant("Rogers"),
				"RegistrationState": dbus.MakeVariant(uint32(1)),
			},
		},
	}
	m, err := Parse(objs)
	if err != nil {
		t.Fatal(err)
	}
	if m.Model != "SIMCOM_SIM7600G-H" || m.State != "connected" || m.Locked != "" || m.Signal != 72 || m.Tech != "LTE" ||
		m.Operator != "Rogers" || m.Registration != "home" || m.Roaming {
		t.Fatalf("parsed %+v", m)
	}
	mod := objs["/org/freedesktop/ModemManager1/Modem/0"]
	mod["org.freedesktop.ModemManager1.Modem"]["State"] = dbus.MakeVariant(int32(2))
	mod["org.freedesktop.ModemManager1.Modem"]["UnlockRequired"] = dbus.MakeVariant(uint32(2))
	mod["org.freedesktop.ModemManager1.Modem"]["AccessTechnologies"] = dbus.MakeVariant(uint32(1 << 9))
	mod["org.freedesktop.ModemManager1.Modem.Modem3gpp"]["RegistrationState"] = dbus.MakeVariant(uint32(5))
	m, _ = Parse(objs)
	if m.State != "locked" || m.Locked != "sim-pin" || m.Tech != "3G" || !m.Roaming {
		t.Fatalf("locked, roaming: %+v", m)
	}
}
