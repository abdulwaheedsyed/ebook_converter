package main

// The macOS application bundle: Leafbind.app, which opens the graphical
// interface when double-clicked, with no Terminal window.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// icnsTypes are the PNG image types of an .icns file, with their sizes in
// pixels: @1x and @2x variants from 16 to 512 points.
var icnsTypes = []struct {
	typ  string
	size int
}{
	{"icp4", 16}, {"ic11", 32}, {"icp5", 32}, {"ic12", 64}, {"ic07", 128},
	{"ic13", 256}, {"ic08", 256}, {"ic14", 512}, {"ic09", 512}, {"ic10", 1024},
}

// macIcon draws the icon as macOS icons are drawn: the shape inset on the
// canvas, 824 of 1024 units, over a soft shadow.
func macIcon(ic *icon, size int) *image.RGBA {
	const body = 824.0 / 1024
	art := ic.render(size, body)
	dst := image.NewRGBA(art.Bounds())

	// The shadow is the icon's own outline, darkened, blurred and dropped.
	sh := image.NewAlpha(art.Bounds())
	dy := max(1, size*12/1024)
	for y := 0; y < size-dy; y++ {
		for x := 0; x < size; x++ {
			sh.Pix[(y+dy)*sh.Stride+x] = uint8(uint32(art.Pix[y*art.Stride+x*4+3]) * 3 / 10)
		}
	}
	blur(sh, max(1, size*10/1024))
	draw.DrawMask(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, sh, image.Point{}, draw.Over)
	draw.Draw(dst, dst.Bounds(), art, image.Point{}, draw.Over)
	return dst
}

// blur is three passes of a box blur, close to a Gaussian.
func blur(a *image.Alpha, r int) {
	w, h := a.Rect.Dx(), a.Rect.Dy()
	tmp := make([]uint8, len(a.Pix))
	pass := func(src, dst []uint8, n, m, step, stride int) {
		for j := range m {
			sum := 0
			for i := -r; i <= r; i++ {
				sum += int(src[j*stride+min(max(i, 0), n-1)*step])
			}
			for i := range n {
				dst[j*stride+i*step] = uint8(sum / (2*r + 1))
				sum += int(src[j*stride+min(i+r+1, n-1)*step]) - int(src[j*stride+max(i-r, 0)*step])
			}
		}
	}
	for range 3 {
		pass(a.Pix, tmp, w, h, 1, a.Stride)
		pass(tmp, a.Pix, h, w, a.Stride, 1)
	}
}

func icnsFile(ic *icon) ([]byte, error) {
	var body bytes.Buffer
	for _, t := range icnsTypes {
		png, err := encodePNG(macIcon(ic, t.size))
		if err != nil {
			return nil, err
		}
		body.WriteString(t.typ)
		binary.Write(&body, binary.BigEndian, uint32(8+len(png)))
		body.Write(png)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+body.Len()))
	b.Write(body.Bytes())
	return b.Bytes(), nil
}

// shortVersion is the version as Info.plist wants it: numbers only.
func shortVersion(v string) string {
	if m := regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`).FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return "0.0.0"
}

func infoPlist(version string) []byte {
	v := html.EscapeString(shortVersion(version))
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDevelopmentRegion</key>
	<string>en</string>
	<key>CFBundleDisplayName</key>
	<string>Leafbind</string>
	<key>CFBundleExecutable</key>
	<string>leafbind</string>
	<key>CFBundleIconFile</key>
	<string>Leafbind</string>
	<key>CFBundleIdentifier</key>
	<string>io.github.abdulwaheedsyed.leafbind</string>
	<key>CFBundleInfoDictionaryVersion</key>
	<string>6.0</string>
	<key>CFBundleName</key>
	<string>Leafbind</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>%[1]s</string>
	<key>CFBundleVersion</key>
	<string>%[1]s</string>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.productivity</string>
	<key>LSMinimumSystemVersion</key>
	<string>12.0</string>
	<!-- The interface is a browser window; the program itself has no
	     windows or menus, so it runs without a Dock icon. -->
	<key>LSUIElement</key>
	<true/>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>NSHumanReadableCopyright</key>
	<string>Copyright © 2026 Syed Abdul Waheed. MIT License.</string>
</dict>
</plist>
`, v)
}

// writeApp assembles Leafbind.app at dir from a darwin binary.
func writeApp(dir, binary, version string, ic *icon) error {
	icns, err := icnsFile(ic)
	if err != nil {
		return err
	}
	contents := filepath.Join(dir, "Contents")
	for _, d := range []string{"MacOS", "Resources"} {
		if err := os.MkdirAll(filepath.Join(contents, d), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), infoPlist(version), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(contents, "Resources", "Leafbind.icns"), icns, 0o644); err != nil {
		return err
	}
	return copyFile(binary, filepath.Join(contents, "MacOS", "leafbind"), 0o755)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
