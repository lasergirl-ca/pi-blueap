// Command palmlink turns a native ARM module for Palm OS, linked several
// times at different addresses with palm/loader/module.ld, into the
// resources the 68k loader (palm/loader/loader.c) reads.
//
// It needs no relocation records, which binutils 2.14 cannot keep while
// dropping unused sections. Instead it compares links:
//
//   - the reference, at text T and data D;
//   - one with only the code moved (T + dt);
//   - one with only the data moved (D + dd);
//   - a check link with both moved by other amounts.
//
// In both blocks, a word that differs by dt in the second link holds a
// code address; one that differs by dd in the third holds a data
// address; one that differs by dt in the second and by -dd in the third
// is a GOTOFF reference to a function, its address less the data
// block's, which GCC 3.3 makes for the address of a static function.
// Anything else differing is refused, naming the function that holds it.
// The relocations found must turn the reference into the check link
// exactly.
//
//	palmlink -out build -link ref.elf:T:D -link text.elf:T2:D -link data.elf:T:D2 -link check.elf:T3:D3
//
// Output resources, named for build-prc (type and ID in hex):
//
//	armcXXXX.bin  the code, in pieces of up to 65,000 bytes, IDs from -id
//	armdXXXX.bin  the data block's initial bytes, in pieces, IDs from -id
//	armhXXXX.bin  the header and relocations (below), ID -id
//
// The header, big-endian: "TSML", u16 version 2, u16 code pieces, u32
// code size, u16 data pieces, u16 0, u32 data size, u32 zeroed (bss)
// size after it, u32 offset of the global offset table in the data block
// (always 0), u32 relocation count, then one u32 per relocation: bits 31
// and 30 what the loader adds (0 the code's address, 1 the data block's,
// 2 the code's less the data block's), bit 29 set when the word is in the
// code, and bits 28 to 0 the offset of the little-endian word. The
// relocated words hold offsets from the start of their blocks.
package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	pieceMax  = 60000 // under the Palm-hosted DLP server's block (palm/sync/dlp/dlp.h), so CradleSync can install itself
	entryName = "ArmletMain"
	version   = 2
)

type link struct {
	path       string
	text, data uint32 // bases it was linked at
}

type image struct {
	text, data []byte
	bss        uint32
	got        uint32 // offset in the data block
	funcs      []elf.Symbol
}

type links []link

func (l *links) String() string { return fmt.Sprint(*l) }

func (l *links) Set(s string) error {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return errors.New("want file:textbase:database")
	}
	t, err := strconv.ParseUint(parts[1], 0, 32)
	if err != nil {
		return err
	}
	d, err := strconv.ParseUint(parts[2], 0, 32)
	if err != nil {
		return err
	}
	*l = append(*l, link{parts[0], uint32(t), uint32(d)})
	return nil
}

func load(l link) (*image, error) {
	f, err := elf.Open(l.path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if f.Machine != elf.EM_ARM || f.Class != elf.ELFCLASS32 || f.Data != elf.ELFDATA2LSB {
		return nil, errors.New("not a little-endian 32-bit ARM ELF file")
	}
	im := &image{}
	for _, s := range f.Sections {
		if s.Flags&elf.SHF_ALLOC == 0 || s.Size == 0 {
			continue
		}
		switch s.Name {
		case ".text":
			if s.Addr != uint64(l.text) {
				return nil, fmt.Errorf(".text at %#x, not %#x", s.Addr, l.text)
			}
			if im.text, err = s.Data(); err != nil {
				return nil, err
			}
		case ".data":
			if s.Addr != uint64(l.data) {
				return nil, fmt.Errorf(".data at %#x, not %#x", s.Addr, l.data)
			}
			if im.data, err = s.Data(); err != nil {
				return nil, err
			}
		case ".bss":
			im.bss = uint32(s.Size)
		default:
			return nil, fmt.Errorf("section %s (%d bytes) is neither code nor data; module.ld must place it", s.Name, s.Size)
		}
	}
	if len(im.text) == 0 {
		return nil, errors.New("no code")
	}
	syms, err := f.Symbols()
	if err != nil {
		return nil, err
	}
	entry := false
	for _, s := range syms {
		if elf.ST_TYPE(s.Info) == elf.STT_FUNC {
			f := s
			f.Value -= uint64(l.text) // as an offset in the code
			im.funcs = append(im.funcs, f)
		}
		switch s.Name {
		case entryName:
			entry = uint32(s.Value) == l.text
		case "_GLOBAL_OFFSET_TABLE_":
			im.got = uint32(s.Value) - l.data
		}
	}
	if !entry {
		return nil, errors.New(entryName + " is not the first byte of the code")
	}
	// Binutils 2.14 measures GOT and GOTOFF references from the start of
	// the output section holding the table, so r10, which the loader sets
	// to the table's address, is right only if the table starts the block.
	if im.got != 0 {
		return nil, fmt.Errorf("the global offset table is at %#x in the data block, not at its start; module.ld must place .got first", im.got)
	}
	if len(im.data)%4 != 0 {
		im.data = append(im.data, make([]byte, 4-len(im.data)%4)...)
	}
	return im, nil
}

// A relocation, as the header lists it: the offset of a little-endian
// word, which block it is in, and what the loader adds to it.
const (
	relocInCode   = 1 << 29 // the word is in the code block, not the data block
	relocKindMask = 3 << 30
	relocCode     = 0 << 30 // add the code block's address
	relocData     = 1 << 30 // add the data block's address
	// relocCodeData adds the code block's address less the data block's:
	// a GOTOFF reference to a function, its address less the GOT's, which
	// GCC 3.3 makes when code takes the address of a static function.
	relocCodeData = 2 << 30
	relocOffMask  = relocInCode - 1
)

// classify says which relocation a word needs, from its value in the
// reference link (w), the link with the code moved by dt (wt) and the one
// with the data moved by dd (wd); ok is false if none explains it.
func classify(w, wt, wd, dt, dd uint32) (kind uint32, needs, ok bool) {
	switch {
	case wt == w && wd == w:
		return 0, false, true
	case wt == w+dt && wd == w:
		return relocCode, true, true
	case wt == w && wd == w+dd:
		return relocData, true, true
	case wt == w+dt && wd == w-dd:
		return relocCodeData, true, true
	}
	return 0, false, false
}

// relocations finds the words of ref's code and data that hold addresses,
// from links with only the code (mt, by dt) or only the data (md, by dd)
// moved.
func relocations(ref, mt, md *image, dt, dd uint32) ([]uint32, error) {
	if len(mt.text) != len(ref.text) || len(md.text) != len(ref.text) || len(ref.text)%4 != 0 {
		return nil, errors.New("the code's size differs between links")
	}
	if len(mt.data) != len(ref.data) || len(md.data) != len(ref.data) || mt.bss != ref.bss || md.bss != ref.bss ||
		mt.got != ref.got || md.got != ref.got {
		return nil, errors.New("the data block's layout differs between links")
	}
	if len(ref.text) > relocOffMask || len(ref.data) > relocOffMask {
		return nil, errors.New("a block too large for the relocation format")
	}
	var out []uint32
	for _, b := range []struct {
		ref, mt, md []byte
		in          uint32
	}{{ref.text, mt.text, md.text, relocInCode}, {ref.data, mt.data, md.data, 0}} {
		for off := 0; off < len(b.ref); off += 4 {
			w := binary.LittleEndian.Uint32(b.ref[off:])
			wt := binary.LittleEndian.Uint32(b.mt[off:])
			wd := binary.LittleEndian.Uint32(b.md[off:])
			kind, needs, ok := classify(w, wt, wd, dt, dd)
			if !ok {
				if b.in != 0 {
					return nil, fmt.Errorf("a code word changes in a way no relocation explains (use -fPIC -msingle-pic-base)%s",
						ref.where(mt))
				}
				return nil, fmt.Errorf("data word at offset %#x changes in a way no relocation explains (%#x, %#x, %#x)", off, w, wt, wd)
			}
			if needs {
				out = append(out, uint32(off)|b.in|kind)
			}
		}
	}
	return out, nil
}

// apply relocates copies of text and data linked at (t0, d0) to (t1, d1).
func apply(text, data []byte, relocs []uint32, t0, d0, t1, d1 uint32) ([]byte, []byte) {
	text, data = bytes.Clone(text), bytes.Clone(data)
	for _, r := range relocs {
		block := data
		if r&relocInCode != 0 {
			block = text
		}
		off := r & relocOffMask
		w := binary.LittleEndian.Uint32(block[off:])
		switch r & relocKindMask {
		case relocCode:
			w += t1 - t0
		case relocData:
			w += d1 - d0
		case relocCodeData:
			w += (t1 - t0) - (d1 - d0)
		}
		binary.LittleEndian.PutUint32(block[off:], w)
	}
	return text, data
}

func pieces(b []byte) [][]byte {
	var out [][]byte
	for len(b) > pieceMax {
		out = append(out, b[:pieceMax])
		b = b[pieceMax:]
	}
	if len(b) > 0 {
		out = append(out, b)
	}
	return out
}

func run(out string, id int, l links) error {
	if len(l) != 4 {
		return errors.New("want four -link flags: reference, code moved, data moved, check")
	}
	ims := make([]*image, 4)
	for i, x := range l {
		im, err := load(x)
		if err != nil {
			return fmt.Errorf("%s: %w", x.path, err)
		}
		ims[i] = im
	}
	ref, check := l[0], l[3]
	if l[1].data != ref.data || l[1].text == ref.text || l[2].text != ref.text || l[2].data == ref.data {
		return errors.New("the second link must move only the code, and the third only the data")
	}
	relocs, err := relocations(ims[0], ims[1], ims[2], l[1].text-ref.text, l[2].data-ref.data)
	if err != nil {
		return err
	}
	if t, d := apply(ims[0].text, ims[0].data, relocs, ref.text, ref.data, check.text, check.data); !bytes.Equal(t, ims[3].text) || !bytes.Equal(d, ims[3].data) {
		return errors.New("the relocations do not reproduce the check link")
	}

	// The loader adds the block's address to each relocated word, so the
	// words it gets hold offsets from their block: the reference link's
	// addresses, moved to code and data at 0.
	text, dataImage := apply(ims[0].text, ims[0].data, relocs, ref.text, ref.data, 0, 0)
	code, data := pieces(text), pieces(dataImage)
	if id+len(code) > 0xffff || id+len(data) > 0xffff {
		return errors.New("resource IDs out of range")
	}
	var h bytes.Buffer
	h.WriteString("TSML")
	_ = binary.Write(&h, binary.BigEndian, uint16(version))
	_ = binary.Write(&h, binary.BigEndian, uint16(len(code)))
	_ = binary.Write(&h, binary.BigEndian, uint32(len(ims[0].text)))
	_ = binary.Write(&h, binary.BigEndian, uint16(len(data)))
	_ = binary.Write(&h, binary.BigEndian, uint16(0))
	_ = binary.Write(&h, binary.BigEndian, uint32(len(ims[0].data)))
	_ = binary.Write(&h, binary.BigEndian, ims[0].bss)
	_ = binary.Write(&h, binary.BigEndian, ims[0].got)
	_ = binary.Write(&h, binary.BigEndian, uint32(len(relocs)))
	for _, r := range relocs {
		_ = binary.Write(&h, binary.BigEndian, r)
	}
	if h.Len() > pieceMax {
		return fmt.Errorf("%d relocations do not fit one header resource", len(relocs))
	}
	write := func(kind string, n int, b []byte) error {
		return os.WriteFile(filepath.Join(out, fmt.Sprintf("%s%04x.bin", kind, id+n)), b, 0o644)
	}
	for i, p := range code {
		if err := write("armc", i, p); err != nil {
			return err
		}
	}
	for i, p := range data {
		if err := write("armd", i, p); err != nil {
			return err
		}
	}
	if err := write("armh", 0, h.Bytes()); err != nil {
		return err
	}
	fmt.Printf("palmlink: code %d bytes in %d pieces, data %d + %d zeroed bytes, %d relocations\n",
		len(ims[0].text), len(code), len(ims[0].data), ims[0].bss, len(relocs))
	return nil
}

func main() {
	out := flag.String("out", ".", "directory for the resource files")
	id := flag.Int("id", 1000, "first resource ID")
	var l links
	flag.Var(&l, "link", "file:textbase:database, four times")
	flag.Parse()
	if err := run(*out, *id, l); err != nil {
		fmt.Fprintln(os.Stderr, "palmlink:", err)
		os.Exit(1)
	}
}

// where names the functions holding the first few code words that differ
// from other's.
func (im *image) where(other *image) string {
	if len(im.text) != len(other.text) {
		return fmt.Sprintf("; the code is %d bytes in one link and %d in another", len(im.text), len(other.text))
	}
	var out []string
	seen := map[string]bool{}
	for off := 0; off+4 <= len(im.text) && len(out) < 8; off += 4 {
		if bytes.Equal(im.text[off:off+4], other.text[off:off+4]) {
			continue
		}
		name := "?"
		for _, s := range im.funcs {
			if s.Value <= uint64(off) && uint64(off) < s.Value+s.Size {
				name = s.Name
			}
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, fmt.Sprintf("%s (+%#x)", name, off))
		}
	}
	return "; in " + strings.Join(out, ", ")
}
