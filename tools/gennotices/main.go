// Command gennotices writes THIRD_PARTY_NOTICES.md: the licenses of every
// third-party component compiled into Leafbind.
//
// Run it from the repository root with "make notices". Go modules are found
// with "go list -deps" for every release platform, so a module linked on one
// platform only is still covered, and their license files are read from the
// module cache. Components inside the embedded PDFium WebAssembly module have
// no module directory; their texts are vendored under third_party/licenses.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// platforms must match PLATFORMS in the Makefile.
var platforms = []string{
	"linux/amd64", "linux/arm64",
	"darwin/amd64", "darwin/arm64",
	"windows/amd64", "windows/arm64",
}

// component is one entry in the notices.
type component struct {
	Name    string
	Version string
	URL     string
	License string   // SPDX-style summary for the index
	Files   []string // license texts, in order
	Note    string
}

// Components whose material is carried in Leafbind's own source.
var sourceComponents = []component{
	{Name: "EPUBCheck", Version: "5.4.0", URL: "https://github.com/w3c/epubcheck", License: "BSD-3-Clause",
		Files: []string{"epubcheck/LICENSE.md"},
		Note: "internal/check reports EPUBCheck's message identifiers and severities and uses its English message " +
			"texts, generated from its source, and reproduces the behaviour of its checks."},
	{Name: "EPUB 3 XHTML schema", Version: "from EPUBCheck 5.4.0", URL: "https://github.com/w3c/epubcheck/tree/main/src/main/resources/com/adobe/epubcheck/schema/30",
		License: "MIT", Files: []string{"internal/check/schema/xhtml/LICENSE"},
		Note: "The RELAX NG schema internal/check validates content documents against, embedded unchanged."},
	{Name: "RELAX NG schema for (X)HTML 5", Version: "from EPUBCheck 5.4.0", URL: "https://github.com/validator/validator/tree/main/schema",
		License: "MIT", Files: []string{"internal/check/schema/xhtml/mod/html5/LICENSE"}},
	{Name: "MathML 3 schema", Version: "from EPUBCheck 5.4.0", URL: "https://www.w3.org/Math/RelaxNG/",
		License: "W3C Software Notice and License", Files: []string{"internal/check/schema/xhtml/mod/mathml/LICENSE", "w3c/software-2002.txt"},
		Note: "The schema files are embedded unchanged."},
	{Name: "ITS 2.0 schema for HTML5", Version: "from EPUBCheck 5.4.0", URL: "https://www.w3.org/TR/its20/",
		License: "MIT", Files: []string{"internal/check/schema/xhtml/mod/its2/LICENSE"}},
}

// Components compiled into the PDFium WebAssembly module embedded by
// go-pdfium. The list follows the license bundle of the PDFium WebAssembly
// build this module derives from. Some entries may be compiled out of this
// configuration or leave no trace in the binary (libpng, which PDFium uses
// only with XFA; fast_float and simdutf, which are header-only); they are
// listed anyway, because an extra notice is harmless and a missing one is not.
var wasmComponents = []component{
	{Name: "PDFium", URL: "https://pdfium.googlesource.com/pdfium/", License: "BSD-3-Clause, with portions under Apache-2.0", Files: []string{"pdfium/pdfium.txt"}},
	{Name: "Abseil", URL: "https://abseil.io/", License: "Apache-2.0", Files: []string{"pdfium/abseil.txt"}},
	{Name: "Anti-Grain Geometry 2.3", URL: "https://agg.sourceforge.net/antigrain.com/", License: "AGG 2.3 license (permissive)", Files: []string{"pdfium/agg23.txt"}},
	{Name: "fast_float", URL: "https://github.com/fastfloat/fast_float", License: "MIT", Files: []string{"pdfium/fast_float.txt"}},
	{Name: "FreeType", URL: "https://freetype.org/", License: "FTL", Files: []string{"pdfium/freetype.txt"},
		Note: "This software is based in part on the work of the FreeType Team."},
	{Name: "ICU", URL: "https://icu.unicode.org/", License: "Unicode-3.0", Files: []string{"pdfium/icu.txt"}},
	{Name: "Little CMS", URL: "https://www.littlecms.com/", License: "MIT", Files: []string{"pdfium/lcms.txt"}},
	{Name: "libjpeg-turbo", URL: "https://libjpeg-turbo.org/", License: "IJG AND BSD-3-Clause AND Zlib", Files: []string{"pdfium/libjpeg_turbo.md", "pdfium/libjpeg_turbo.ijg"},
		Note: "This software is based in part on the work of the Independent JPEG Group."},
	{Name: "libpng", URL: "http://www.libpng.org/pub/png/libpng.html", License: "libpng-2.0", Files: []string{"pdfium/libpng.txt"}},
	{Name: "OpenJPEG", URL: "https://www.openjpeg.org/", License: "BSD-2-Clause", Files: []string{"pdfium/libopenjpeg.txt"}},
	{Name: "simdutf", URL: "https://github.com/simdutf/simdutf", License: "MIT", Files: []string{"pdfium/simdutf.txt"}},
	{Name: "zlib", URL: "https://zlib.net/", License: "Zlib", Files: []string{"pdfium/zlib.txt"}},
	{Name: "Emscripten runtime and musl libc", URL: "https://emscripten.org/", License: "MIT OR NCSA (Emscripten); MIT (musl)",
		Files: []string{"emscripten/LICENSE", "emscripten/musl-COPYRIGHT"},
		Note: "The C runtime linked into the WebAssembly module. Its LLVM C++ runtime (libc++, libc++abi, compiler-rt) is " +
			"Apache-2.0 WITH LLVM-exception, which waives the notice requirement for compiled binaries."},
}

func main() {
	out := flag.String("o", "THIRD_PARTY_NOTICES.md", "output file")
	flag.Parse()
	log.SetFlags(0)

	modules, err := linkedModules()
	if err != nil {
		log.Fatal(err)
	}

	// Go's license is vendored too: some distributions move LICENSE out of
	// GOROOT, so reading it from there is not portable.
	comps := []component{{
		Name: "Go standard library and runtime", URL: "https://go.dev/", License: "BSD-3-Clause",
		Files: []string{"third_party/licenses/go/LICENSE", "third_party/licenses/go/PATENTS"},
	}}
	for _, m := range modules {
		files := licenseFiles(m.Dir)
		if len(files) == 0 {
			log.Fatalf("%s %s: no license file found in %s", m.Path, m.Version, m.Dir)
		}
		comps = append(comps, component{
			Name: m.Path, Version: m.Version, URL: "https://" + m.Path,
			License: identify(files[0]), Files: files,
		})
	}
	for _, c := range sourceComponents {
		for i, f := range c.Files {
			// Licenses of vendored files sit beside them; the rest are in third_party.
			if !strings.HasPrefix(f, "internal/") {
				c.Files[i] = filepath.Join("third_party", "licenses", f)
			}
		}
		comps = append(comps, c)
	}
	sourceCount := len(sourceComponents)

	// The WebAssembly components arrive inside go-pdfium; say which version.
	via := ""
	for _, m := range modules {
		if m.Path == "github.com/klippa-app/go-pdfium" {
			via = "in go-pdfium " + m.Version
		}
	}
	for _, c := range wasmComponents {
		c.Version = via
		for i, f := range c.Files {
			c.Files[i] = filepath.Join("third_party", "licenses", f)
		}
		comps = append(comps, c)
	}

	var b bytes.Buffer
	write(&b, comps, len(modules)+1, sourceCount)
	if err := os.WriteFile(*out, b.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %d components\n", *out, len(comps))
}

type module struct{ Path, Version, Dir string }

// linkedModules returns the non-main modules that provide packages to the
// program, across every release platform.
func linkedModules() ([]module, error) {
	seen := map[string]module{}
	for _, p := range platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		cmd := exec.Command("go", "list", "-deps",
			"-f", `{{with .Module}}{{if not .Main}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}{{end}}`, ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
		cmd.Stderr = os.Stderr
		outb, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s: %w", p, err)
		}
		for _, line := range strings.Split(string(outb), "\n") {
			if parts := strings.Split(line, "|"); len(parts) == 3 {
				seen[parts[0]] = module{parts[0], parts[1], parts[2]}
			}
		}
	}
	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

var licenseName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|patents)(\.(txt|md))?$`)

// licenseFiles returns a module's license, notice and patent files, license
// first, since Apache-2.0 requires any NOTICE file to be reproduced too.
func licenseFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	rank := func(name string) int {
		switch n := strings.ToLower(name); {
		case strings.HasPrefix(n, "licen"), strings.HasPrefix(n, "copying"):
			return 0
		case strings.HasPrefix(n, "notice"):
			return 1
		default:
			return 2
		}
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && licenseName.MatchString(e.Name()) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		return rank(filepath.Base(files[i])) < rank(filepath.Base(files[j]))
	})
	return files
}

// identify names a common license for the index. The full text below it is
// what counts; this only saves the reader from opening it.
func identify(file string) string {
	t, _ := os.ReadFile(file)
	s := string(t)
	switch {
	case strings.Contains(s, "Apache License") && strings.Contains(s, "Version 2.0"):
		return "Apache-2.0"
	case strings.Contains(s, "Permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(s, "Redistribution and use in source and binary forms") && strings.Contains(s, "Neither the name"):
		return "BSD-3-Clause"
	case strings.Contains(s, "Redistribution and use in source and binary forms"):
		return "BSD-2-Clause"
	}
	return "see text"
}

func write(b *bytes.Buffer, comps []component, goCount, sourceCount int) {
	b.WriteString(`# Third-party notices

Leafbind is distributed under the MIT License; see LICENSE. Its
executables also contain the third-party software listed here, each under its
own license, reproduced in full below.

The PDF engine is PDFium, compiled to WebAssembly and embedded through
go-pdfium. The components from PDFium onward in the table are compiled into
that WebAssembly module. The built-in EPUB validation carries material from
EPUBCheck, including the RELAX NG schemas it validates content documents
against.

This software is based in part on the work of the FreeType Team.

This software is based in part on the work of the Independent JPEG Group.

This file is generated by ` + "`make notices`" + `; do not edit it by hand.

| Component | Version | License |
| --- | --- | --- |
`)
	for _, c := range comps {
		v := c.Version
		if v == "" {
			v = "—"
		}
		fmt.Fprintf(b, "| [%s](#%s) | %s | %s |\n", c.Name, anchor(c.Name), v, c.License)
	}

	for i, c := range comps {
		switch i {
		case goCount:
			b.WriteString("\n---\n\n## Material carried in Leafbind's source\n")
		case goCount + sourceCount:
			b.WriteString("\n---\n\n## Components of the embedded PDFium WebAssembly module\n")
		}
		fmt.Fprintf(b, "\n---\n\n### %s\n\n", c.Name)
		if c.Version != "" {
			fmt.Fprintf(b, "Version: %s  \n", c.Version)
		}
		fmt.Fprintf(b, "Source: <%s>  \nLicense: %s\n", c.URL, c.License)
		if c.Note != "" {
			fmt.Fprintf(b, "\n%s\n", c.Note)
		}
		for _, f := range c.Files {
			text, err := os.ReadFile(f)
			if err != nil {
				log.Fatal(err)
			}
			fence := "```"
			for strings.Contains(string(text), fence) {
				fence += "`"
			}
			fmt.Fprintf(b, "\n%s:\n\n%stext\n%s\n%s\n", filepath.Base(f), fence, strings.TrimRight(string(text), "\n"), fence)
		}
	}
}

// anchor mirrors GitHub's heading anchors: lower case, punctuation other than
// hyphens and underscores dropped, spaces to hyphens.
func anchor(s string) string {
	var r strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			r.WriteRune(c)
		case c == ' ':
			r.WriteByte('-')
		}
	}
	return r.String()
}
