package main

// The Linux desktop entry and icons, so Leafbind appears in the
// applications menu once installed.

import (
	"os"
	"path/filepath"
)

const desktopEntry = `[Desktop Entry]
Type=Application
Name=Leafbind
GenericName=PDF to EPUB Converter
Comment=Turn PDFs into fixed-layout EPUB books for Kindle
Exec=leafbind --gui
Icon=leafbind
Terminal=false
Categories=Office;Publishing;
Keywords=PDF;EPUB;Kindle;ebook;convert;
StartupNotify=false
`

// writeLinux writes leafbind.desktop, leafbind.svg and leafbind.png to dir.
func writeLinux(dir, svgPath string, ic *icon) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "leafbind.desktop"), []byte(desktopEntry), 0o644); err != nil {
		return err
	}
	if err := copyFile(svgPath, filepath.Join(dir, "leafbind.svg"), 0o644); err != nil {
		return err
	}
	png, err := encodePNG(ic.render(256, 1))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "leafbind.png"), png, 0o644)
}
