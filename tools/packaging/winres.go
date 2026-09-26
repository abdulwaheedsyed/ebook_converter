package main

// Windows resources: the program's icon and version information, written
// as a COFF object (.syso) that the Go linker adds to the executable. The
// format is small enough to write directly, so no Windows toolchain and no
// resource compiler is needed.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf16"
)

// Resource types.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16

	langEnUS = 0x0409
)

// COFF machine types, and the relocation that makes an address relative to
// the image base for each.
var machines = map[string]struct{ machine, reloc uint16 }{
	"amd64": {0x8664, 0x0003}, // IMAGE_REL_AMD64_ADDR32NB
	"arm64": {0xAA64, 0x0002}, // IMAGE_REL_ARM64_ADDR32NB
}

// icoSizes are the sizes Windows asks for, from list views to large icons.
var icoSizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

type resource struct {
	typ, id int
	data    []byte
}

// iconResources holds each size as a PNG, which Windows reads since Vista,
// and the group that lists them.
func iconResources(pngs map[int][]byte) []resource {
	var res []resource
	var group bytes.Buffer
	binary.Write(&group, binary.LittleEndian, [3]uint16{0, 1, uint16(len(icoSizes))})
	for i, size := range icoSizes {
		id := i + 1
		res = append(res, resource{rtIcon, id, pngs[size]})
		dim := uint8(size)
		if size >= 256 {
			dim = 0 // 0 means 256
		}
		group.Write([]byte{dim, dim, 0, 0})
		binary.Write(&group, binary.LittleEndian, struct {
			Planes, BitCount uint16
			Size             uint32
			ID               uint16
		}{1, 32, uint32(len(pngs[size])), uint16(id)})
	}
	return append(res, resource{rtGroupIcon, 1, group.Bytes()})
}

// icoFile is the same icons as a standalone .ico file.
func icoFile(pngs map[int][]byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(icoSizes))})
	off := 6 + 16*len(icoSizes)
	for _, size := range icoSizes {
		dim := uint8(size)
		if size >= 256 {
			dim = 0
		}
		b.Write([]byte{dim, dim, 0, 0})
		binary.Write(&b, binary.LittleEndian, struct {
			Planes, BitCount uint16
			Size, Offset     uint32
		}{1, 32, uint32(len(pngs[size])), uint32(off)})
		off += len(pngs[size])
	}
	for _, size := range icoSizes {
		b.Write(pngs[size])
	}
	return b.Bytes()
}

// versionNumbers reads "v1.2.3" and "v1.2.3-4-gabcdef" as 1.2.3.0; anything
// else is 0.0.0.0.
func versionNumbers(v string) [4]uint16 {
	m := regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(v)
	var n [4]uint16
	for i := 1; i < len(m); i++ {
		x, _ := strconv.Atoi(m[i])
		n[i-1] = uint16(x)
	}
	return n
}

// versionResource builds a VS_VERSIONINFO block.
func versionResource(version string, strings [][2]string) []byte {
	n := versionNumbers(version)
	ms, ls := uint32(n[0])<<16|uint32(n[1]), uint32(n[2])<<16|uint32(n[3])
	var fixed bytes.Buffer
	binary.Write(&fixed, binary.LittleEndian, [13]uint32{
		0xFEEF04BD, 0x00010000, // signature, structure version
		ms, ls, ms, ls, // file and product version
		0x3F, 0, // flags mask, flags
		0x00040004, // VOS_NT_WINDOWS32
		1,          // VFT_APP
		0, 0, 0,    // subtype, date
	})

	var table []*vnode
	for _, kv := range strings {
		table = append(table, &vnode{key: kv[0], text: true, value: utf16z(kv[1])})
	}
	translation := []byte{0x09, 0x04, 0xB0, 0x04} // en-US, Unicode
	root := &vnode{key: "VS_VERSION_INFO", value: fixed.Bytes(), children: []*vnode{
		{key: "StringFileInfo", children: []*vnode{{key: "040904B0", children: table}}},
		{key: "VarFileInfo", children: []*vnode{{key: "Translation", value: translation}}},
	}}
	return root.bytes()
}

// vnode is one block of a version resource: a header, a key, a value and
// children, each aligned to 32 bits.
type vnode struct {
	key      string
	value    []byte
	text     bool
	children []*vnode
}

func utf16z(s string) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, append(utf16.Encode([]rune(s)), 0))
	return b.Bytes()
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func (v *vnode) bytes() []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 6)) // wLength, wValueLength, wType, filled in below
	b.Write(utf16z(v.key))
	pad4(&b)
	b.Write(v.value)
	for _, c := range v.children {
		pad4(&b)
		b.Write(c.bytes())
	}
	out := b.Bytes()
	valueLen, typ := len(v.value), uint16(0)
	if v.text {
		valueLen, typ = len(v.value)/2, 1 // in characters, with the terminator
	}
	binary.LittleEndian.PutUint16(out[0:], uint16(len(out)))
	binary.LittleEndian.PutUint16(out[2:], uint16(valueLen))
	binary.LittleEndian.PutUint16(out[4:], typ)
	return out
}

// coffResources writes resources as a COFF object with one .rsrc section.
// The section holds the three-level resource tree (type, id, language),
// then the data entries, then the data; each data entry's address is a
// relocation the linker resolves against the section.
func coffResources(res []resource, arch string) ([]byte, error) {
	mach, ok := machines[arch]
	if !ok {
		return nil, fmt.Errorf("no COFF machine type for %s", arch)
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].typ != res[j].typ {
			return res[i].typ < res[j].typ
		}
		return res[i].id < res[j].id
	})
	var types []int
	byType := map[int][]int{} // type -> indices into res
	for i, r := range res {
		if len(byType[r.typ]) == 0 {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], i)
	}

	// Lay out the tree: the root, a directory per type, a directory per
	// resource (holding its one language), then the data entries.
	const dirSize, entrySize, dataEntrySize = 16, 8, 16
	rootSize := dirSize + entrySize*len(types)
	typeOff := map[int]int{}
	off := rootSize
	for _, t := range types {
		typeOff[t] = off
		off += dirSize + entrySize*len(byType[t])
	}
	langOff := make([]int, len(res))
	for i := range res {
		langOff[i] = off
		off += dirSize + entrySize
	}
	dataEntryOff := make([]int, len(res))
	for i := range res {
		dataEntryOff[i] = off
		off += dataEntrySize
	}
	dataOff := make([]int, len(res))
	for i, r := range res {
		off = (off + 7) &^ 7
		dataOff[i] = off
		off += len(r.data)
	}
	sect := make([]byte, (off+7)&^7)

	le := binary.LittleEndian
	dir := func(at, ids int) { le.PutUint16(sect[at+14:], uint16(ids)) }
	entry := func(at, id, target int, subdir bool) {
		le.PutUint32(sect[at:], uint32(id))
		t := uint32(target)
		if subdir {
			t |= 0x80000000
		}
		le.PutUint32(sect[at+4:], t)
	}
	dir(0, len(types))
	for k, t := range types {
		entry(dirSize+entrySize*k, t, typeOff[t], true)
		dir(typeOff[t], len(byType[t]))
		for m, i := range byType[t] {
			entry(typeOff[t]+dirSize+entrySize*m, res[i].id, langOff[i], true)
		}
	}
	var relocs bytes.Buffer
	for i, r := range res {
		dir(langOff[i], 1)
		entry(langOff[i]+dirSize, langEnUS, dataEntryOff[i], false)
		le.PutUint32(sect[dataEntryOff[i]:], uint32(dataOff[i])) // + the section's address, by relocation
		le.PutUint32(sect[dataEntryOff[i]+4:], uint32(len(r.data)))
		copy(sect[dataOff[i]:], r.data)
		binary.Write(&relocs, le, struct {
			VirtualAddress, Symbol uint32
			Type                   uint16
		}{uint32(dataEntryOff[i]), 0, mach.reloc})
	}

	const fileHeader, sectionHeader = 20, 40
	rawAt := fileHeader + sectionHeader
	relocAt := rawAt + len(sect)
	symAt := relocAt + relocs.Len()

	var b bytes.Buffer
	binary.Write(&b, le, struct {
		Machine, Sections              uint16
		Time, SymbolTable, SymbolCount uint32
		OptionalHeader, Flags          uint16
	}{mach.machine, 1, 0, uint32(symAt), 1, 0, 0})
	binary.Write(&b, le, struct {
		Name                            [8]byte
		VirtualSize, VirtualAddress     uint32
		RawSize, RawAt, RelocAt, LineAt uint32
		RelocCount, LineCount           uint16
		Flags                           uint32
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 0, uint32(len(sect)), uint32(rawAt), uint32(relocAt), 0,
		uint16(len(res)), 0, 0x40000040}) // initialised data, readable
	b.Write(sect)
	b.Write(relocs.Bytes())
	// One symbol, the section itself, which the relocations refer to.
	binary.Write(&b, le, struct {
		Name          [8]byte
		Value         uint32
		Section       int16
		Type          uint16
		Class, NumAux uint8
	}{[8]byte{'.', 'r', 's', 'r', 'c'}, 0, 1, 0, 3, 0}) // IMAGE_SYM_CLASS_STATIC
	binary.Write(&b, le, uint32(4)) // an empty string table
	return b.Bytes(), nil
}
