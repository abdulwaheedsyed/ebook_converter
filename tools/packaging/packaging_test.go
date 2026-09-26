package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"image/color"
	"os"
	"testing"
)

func loadIcon(t *testing.T) *icon {
	t.Helper()
	f, err := os.Open("../../" + iconSVG)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ic, err := parseIcon(f)
	if err != nil {
		t.Fatal(err)
	}
	return ic
}

// The rendering has the icon's shape and colours: transparent rounded
// corners, the gradient from light to dark, and the white left-hand page.
func TestRenderIcon(t *testing.T) {
	m := loadIcon(t).render(256, 1)
	at := func(x, y int) color.RGBA { return m.RGBAAt(x, y) }
	if c := at(1, 1); c.A != 0 {
		t.Errorf("corner is %v, want transparent", c)
	}
	if tl, br := at(40, 40), at(215, 215); tl.A != 255 || br.A != 255 || tl.R <= br.R {
		t.Errorf("gradient runs %v to %v, want opaque, lighter at the top left", tl, br)
	}
	if c := at(80, 128); c != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("left page is %v, want white", c)
	}
	if c := at(128, 106); c.B < 150 || c.R > 120 {
		t.Errorf("stitch is %v, want indigo", c)
	}
}

// The COFF object is one .rsrc section whose resource tree finds every
// resource's data, with a relocation for each data entry.
func TestCOFFResources(t *testing.T) {
	res := []resource{{rtVersion, 1, []byte("version")}, {rtIcon, 2, []byte("two")}, {rtIcon, 1, []byte("one!")}}
	for arch, m := range machines {
		obj, err := coffResources(append([]resource(nil), res...), arch)
		if err != nil {
			t.Fatal(err)
		}
		f, err := pe.NewFile(bytes.NewReader(obj))
		if err != nil {
			t.Fatalf("%s: %v", arch, err)
		}
		if f.Machine != m.machine || len(f.Sections) != 1 || f.Sections[0].Name != ".rsrc" {
			t.Fatalf("%s: machine %#x, sections %v", arch, f.Machine, f.Sections)
		}
		s := f.Sections[0]
		if len(s.Relocs) != len(res) {
			t.Errorf("%s: %d relocations, want %d", arch, len(s.Relocs), len(res))
		}
		data, _ := s.Data()
		le := binary.LittleEndian
		// Walk type -> id -> language -> data entry.
		found := map[[2]int]string{}
		dir := func(off int) [][2]uint32 {
			n := int(le.Uint16(data[off+12:]) + le.Uint16(data[off+14:]))
			var out [][2]uint32
			for i := range n {
				e := off + 16 + 8*i
				out = append(out, [2]uint32{le.Uint32(data[e:]), le.Uint32(data[e+4:])})
			}
			return out
		}
		for _, te := range dir(0) {
			for _, ie := range dir(int(te[1] &^ 0x80000000)) {
				le1 := dir(int(ie[1] &^ 0x80000000))[0]
				de := int(le1[1])
				at, size := le.Uint32(data[de:]), le.Uint32(data[de+4:])
				found[[2]int{int(te[0]), int(ie[0])}] = string(data[at : at+size])
			}
		}
		for _, r := range res {
			if got := found[[2]int{r.typ, r.id}]; got != string(r.data) {
				t.Errorf("%s: resource %d/%d reads %q, want %q", arch, r.typ, r.id, got, r.data)
			}
		}
	}
}

func TestVersions(t *testing.T) {
	for in, want := range map[string][4]uint16{
		"v1.2.3": {1, 2, 3, 0}, "0.5.0": {0, 5, 0, 0}, "v1.2.3-4-gabcdef": {1, 2, 3, 0}, "dev": {}, "abc123": {},
	} {
		if got := versionNumbers(in); got != want {
			t.Errorf("versionNumbers(%q) = %v, want %v", in, got, want)
		}
	}
	if got := shortVersion("v0.6.0-2-g1234567-dirty"); got != "0.6.0" {
		t.Errorf("shortVersion = %q", got)
	}
	v := versionResource("v1.2.3", [][2]string{{"ProductName", "Leafbind"}})
	if int(binary.LittleEndian.Uint16(v)) != len(v) || !bytes.Contains(v, utf16z("VS_VERSION_INFO")) || !bytes.Contains(v, utf16z("Leafbind")) {
		t.Error("the version resource is malformed")
	}
}

func TestIcoAndIcns(t *testing.T) {
	ic := loadIcon(t)
	pngs, err := icoPNGs(ic)
	if err != nil {
		t.Fatal(err)
	}
	ico := icoFile(pngs)
	if n := binary.LittleEndian.Uint16(ico[4:]); int(n) != len(icoSizes) {
		t.Fatalf("ico has %d images", n)
	}
	for i, size := range icoSizes {
		e := ico[6+16*i:]
		size32, off := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		if !bytes.Equal(ico[off:off+size32], pngs[size]) {
			t.Errorf("ico image %d is not the %d-pixel PNG", i, size)
		}
	}
	icns, err := icnsFile(ic)
	if err != nil {
		t.Fatal(err)
	}
	if string(icns[:4]) != "icns" || int(binary.BigEndian.Uint32(icns[4:])) != len(icns) {
		t.Error("icns header is wrong")
	}
}
