// Package profile implements the three RFCOMM server profiles (SPP, LAP,
// DUN) as org.bluez.Profile1 objects, their SDP records, and the pre-PPP
// handshakes a Palm performs on them (PLAN.md sections 8.3 and 8.4).
package profile

import (
	"fmt"
	"strings"
)

// Kind is one of the three RFCOMM profiles.
type Kind string

// The profiles, by the name used in config, logs and status.
const (
	SPP Kind = "spp"
	LAP Kind = "lap"
	DUN Kind = "dun"
)

// Kinds lists the profiles in registration order.
var Kinds = []Kind{SPP, LAP, DUN}

// UUID is the 128-bit service class UUID BlueZ registers the profile under.
func (k Kind) UUID() string {
	switch k {
	case SPP:
		return "00001101-0000-1000-8000-00805f9b34fb"
	case LAP:
		return "00001102-0000-1000-8000-00805f9b34fb"
	case DUN:
		return "00001103-0000-1000-8000-00805f9b34fb"
	}
	return ""
}

// ServiceName is what the Palm's service picker shows.
func (k Kind) ServiceName() string {
	switch k {
	case SPP:
		return "Serial Port"
	case LAP:
		return "LAN Access using PPP"
	case DUN:
		return "Dial-up Networking"
	}
	return ""
}

// The SDP records from PLAN.md Appendix C. {{CHANNEL}} is substituted.
const (
	sppRecord = `<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1101"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1101"/><uint16 value="0x0102"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="Serial Port"/></attribute>
</record>
`
	lapRecord = `<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1102"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1102"/><uint16 value="0x0100"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="LAN Access using PPP"/></attribute>
  <attribute id="0x0200"><uint16 value="0x0000"/></attribute>
</record>
`
	dunRecord = `<?xml version="1.0" encoding="UTF-8" ?>
<record>
  <attribute id="0x0001"><sequence><uuid value="0x1103"/><uuid value="0x1201"/></sequence></attribute>
  <attribute id="0x0004">
    <sequence>
      <sequence><uuid value="0x0100"/></sequence>
      <sequence><uuid value="0x0003"/><uint8 value="{{CHANNEL}}"/></sequence>
    </sequence>
  </attribute>
  <attribute id="0x0005"><sequence><uuid value="0x1002"/></sequence></attribute>
  <attribute id="0x0009"><sequence><sequence><uuid value="0x1103"/><uint16 value="0x0100"/></sequence></sequence></attribute>
  <attribute id="0x0100"><text value="Dial-up Networking"/></attribute>
  <attribute id="0x0305"><boolean value="false"/></attribute>
</record>
`
)

// Record returns the SDP record XML for k advertising channel.
func Record(k Kind, channel int) (string, error) {
	if channel < 1 || channel > 30 {
		return "", fmt.Errorf("profile %s: RFCOMM channel %d out of range 1-30", k, channel)
	}
	var tmpl string
	switch k {
	case SPP:
		tmpl = sppRecord
	case LAP:
		tmpl = lapRecord
	case DUN:
		tmpl = dunRecord
	default:
		return "", fmt.Errorf("profile: unknown kind %q", k)
	}
	return strings.ReplaceAll(tmpl, "{{CHANNEL}}", fmt.Sprint(channel)), nil
}
