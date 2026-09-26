// Command packaging writes what each platform needs beyond the executable,
// all drawn from the icon in web/icon.svg:
//
//	go run ./tools/packaging winres -version v1.2.3
//	    rsrc_windows_amd64.syso and rsrc_windows_arm64.syso in the current
//	    directory: the icon and version information, which go build then
//	    links into leafbind.exe
//	go run ./tools/packaging app -version v1.2.3 -bin leafbind -o Leafbind.app
//	    a macOS application bundle around a darwin binary
//	go run ./tools/packaging linux -o dir
//	    leafbind.desktop, leafbind.svg and leafbind.png
//	go run ./tools/packaging icons -o dir
//	    the icons alone, as .ico, .icns and PNG, to look at
//
// Run it from the repository root; the Makefile does so when packaging.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

const iconSVG = "web/icon.svg"

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: packaging winres|app|linux|icons [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	version := fs.String("version", "dev", "version to record")
	bin := fs.String("bin", "", "darwin binary to bundle (app)")
	out := fs.String("o", "", "output path")
	fs.Parse(os.Args[2:])

	f, err := os.Open(iconSVG)
	if err != nil {
		log.Fatal(err)
	}
	ic, err := parseIcon(f)
	f.Close()
	if err != nil {
		log.Fatalf("%s: %v", iconSVG, err)
	}

	switch os.Args[1] {
	case "winres":
		pngs, err := icoPNGs(ic)
		if err != nil {
			log.Fatal(err)
		}
		res := append(iconResources(pngs), resource{rtVersion, 1, versionResource(*version, versionStrings(*version))})
		for _, arch := range []string{"amd64", "arm64"} {
			obj, err := coffResources(res, arch)
			if err != nil {
				log.Fatal(err)
			}
			name := "rsrc_windows_" + arch + ".syso"
			if err := os.WriteFile(name, obj, 0o644); err != nil {
				log.Fatal(err)
			}
			fmt.Println("wrote", name)
		}
	case "app":
		if *bin == "" || *out == "" {
			log.Fatal("app needs -bin and -o")
		}
		if err := writeApp(*out, *bin, *version, ic); err != nil {
			log.Fatal(err)
		}
		fmt.Println("wrote", *out)
	case "linux":
		if *out == "" {
			log.Fatal("linux needs -o")
		}
		if err := writeLinux(*out, iconSVG, ic); err != nil {
			log.Fatal(err)
		}
	case "icons":
		if *out == "" {
			log.Fatal("icons needs -o")
		}
		if err := writeIcons(*out, ic); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func versionStrings(version string) [][2]string {
	return [][2]string{
		{"CompanyName", "Syed Abdul Waheed"},
		{"FileDescription", "Leafbind: PDF to Kindle fixed-layout EPUB"},
		{"FileVersion", version},
		{"InternalName", "leafbind"},
		{"LegalCopyright", "Copyright © 2026 Syed Abdul Waheed. MIT License."},
		{"OriginalFilename", "leafbind.exe"},
		{"ProductName", "Leafbind"},
		{"ProductVersion", version},
	}
}

func encodePNG(m image.Image) ([]byte, error) {
	var b bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&b, m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func icoPNGs(ic *icon) (map[int][]byte, error) {
	pngs := map[int][]byte{}
	for _, size := range icoSizes {
		b, err := encodePNG(ic.render(size, 1))
		if err != nil {
			return nil, err
		}
		pngs[size] = b
	}
	return pngs, nil
}

func writeIcons(dir string, ic *icon) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pngs, err := icoPNGs(ic)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "leafbind.ico"), icoFile(pngs), 0o644); err != nil {
		return err
	}
	icns, err := icnsFile(ic)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "Leafbind.icns"), icns, 0o644); err != nil {
		return err
	}
	for _, size := range []int{16, 32, 256, 1024} {
		b, err := encodePNG(ic.render(size, 1))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("leafbind-%d.png", size)), b, 0o644); err != nil {
			return err
		}
		m, err := encodePNG(macIcon(ic, size))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("leafbind-macos-%d.png", size)), m, 0o644); err != nil {
			return err
		}
	}
	return nil
}
