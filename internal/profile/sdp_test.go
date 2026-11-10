package profile

import (
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
)

type xmlAttr struct {
	ID  string `xml:"id,attr"`
	Raw []byte `xml:",innerxml"`
}

type xmlRecord struct {
	Attrs []xmlAttr `xml:"attribute"`
}

// rfcommChannel digs the uint8 that follows uuid 0x0003 (RFCOMM) out of the
// ProtocolDescriptorList.
func rfcommChannel(t *testing.T, raw []byte) string {
	t.Helper()
	type elem struct {
		XMLName xml.Name
		Value   string `xml:"value,attr"`
		Kids    []elem `xml:",any"`
	}
	var root elem
	if err := xml.Unmarshal([]byte("<x>"+string(raw)+"</x>"), &root); err != nil {
		t.Fatal(err)
	}
	var found string
	var walk func(e elem)
	walk = func(e elem) {
		for i, k := range e.Kids {
			if k.XMLName.Local == "uuid" && k.Value == "0x0003" && i+1 < len(e.Kids) && e.Kids[i+1].XMLName.Local == "uint8" {
				found = e.Kids[i+1].Value
			}
			walk(k)
		}
	}
	walk(root)
	return found
}

func TestRecords(t *testing.T) {
	for _, k := range Kinds {
		for _, ch := range []int{1, 3, 30} {
			rec, err := Record(k, ch)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(rec, "{{") {
				t.Errorf("%s: unsubstituted placeholder", k)
			}
			var r xmlRecord
			if err := xml.Unmarshal([]byte(rec), &r); err != nil {
				t.Fatalf("%s ch %d: not well-formed: %v", k, ch, err)
			}
			byID := map[string][]byte{}
			for _, a := range r.Attrs {
				byID[a.ID] = a.Raw
			}
			if got := rfcommChannel(t, byID["0x0004"]); got != strconv.Itoa(ch) {
				t.Errorf("%s: RFCOMM channel in record = %q, want %d", k, got, ch)
			}
			if !strings.Contains(string(byID["0x0100"]), k.ServiceName()) {
				t.Errorf("%s: ServiceName missing", k)
			}
			short := "0x" + strings.ToUpper(k.UUID()[4:8])
			if !strings.Contains(strings.ToUpper(string(byID["0x0001"])), strings.ToUpper(short)) {
				t.Errorf("%s: ServiceClassIDList lacks %s", k, short)
			}
		}
	}
	if _, ok := byIDHas(t, LAP, "0x0200"); !ok {
		t.Error("LAP record lacks IpSubnet (0x0200)")
	}
	if _, err := Record(SPP, 0); err == nil {
		t.Error("channel 0 accepted")
	}
	if _, err := Record(Kind("x"), 1); err == nil {
		t.Error("unknown kind accepted")
	}
}

func byIDHas(t *testing.T, k Kind, id string) ([]byte, bool) {
	t.Helper()
	rec, _ := Record(k, 2)
	var r xmlRecord
	if err := xml.Unmarshal([]byte(rec), &r); err != nil {
		t.Fatal(err)
	}
	for _, a := range r.Attrs {
		if a.ID == id {
			return a.Raw, true
		}
	}
	return nil, false
}
