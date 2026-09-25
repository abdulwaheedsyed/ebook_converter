package main

import (
	"archive/zip"
	"bytes"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

// goodBook packages two tiny greyscale pages without the PDF engine.
func goodBook(t *testing.T) ([]byte, Expect) {
	t.Helper()
	canvas := Size{40, 30}
	var pages []Page
	for range 2 {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, canvas.W, canvas.H)), nil); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, Page{JPEG: buf.Bytes(), Size: canvas, Source: canvas, Orient: "landscape"})
	}
	b := &Book{
		Title: `Test <&> "Book"`, Lang: "en", Direction: "ltr",
		Orient: bookOrientation(canvas, "", false), Canvas: canvas,
		Pages: pages, Modified: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), ID: newUUID(),
	}
	var out bytes.Buffer
	if err := writeEPUB(&out, b); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), Expect{Pages: 2, Canvas: &canvas, Grayscale: true}
}

// rezip rewrites an archive, letting edit change each entry.
func rezip(t *testing.T, data []byte, edit func(name string, body []byte, h *zip.FileHeader) []byte) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		body, _ := readZip(f)
		h := &zip.FileHeader{Name: f.Name, Method: f.Method}
		if body = edit(f.Name, body, h); body == nil {
			continue
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	zw.Close()
	return out.Bytes()
}

func codes(probs []Problem) string {
	var c []string
	for _, p := range probs {
		c = append(c, p.Code)
	}
	return strings.Join(c, " ")
}

func TestValidatePassesGoodBook(t *testing.T) {
	data, want := goodBook(t)
	if probs := validatePackage(data, want); len(probs) != 0 {
		t.Fatalf("a good book failed validation: %v", probs)
	}
}

func TestValidateCatchesProblems(t *testing.T) {
	data, want := goodBook(t)
	cases := []struct {
		name string
		code string
		edit func(name string, body []byte, h *zip.FileHeader) []byte
	}{
		{"whitespace around pre-paginated", "OPF-006", func(n string, b []byte, _ *zip.FileHeader) []byte {
			if n == "OEBPS/content.opf" {
				return bytes.Replace(b, []byte(">pre-paginated<"), []byte(">\n    pre-paginated\n  <"), 1)
			}
			return b
		}},
		{"compressed mimetype", "ZIP-003", func(n string, b []byte, h *zip.FileHeader) []byte {
			if n == "mimetype" {
				h.Method = zip.Deflate
			}
			return b
		}},
		{"missing image", "OPF-009", func(n string, b []byte, _ *zip.FileHeader) []byte {
			if n == "OEBPS/images/page-002.jpg" {
				return nil
			}
			return b
		}},
		{"malformed XHTML", "XML-001", func(n string, b []byte, _ *zip.FileHeader) []byte {
			if n == "OEBPS/text/page-001.xhtml" {
				return bytes.Replace(b, []byte("</body>"), nil, 1)
			}
			return b
		}},
		{"bad language", "OPF-004", func(n string, b []byte, _ *zip.FileHeader) []byte {
			if n == "OEBPS/content.opf" {
				return bytes.Replace(b, []byte("<dc:language>en<"), []byte("<dc:language>not a language<"), 1)
			}
			return b
		}},
		{"multi-line modified date", "OPF-005", func(n string, b []byte, _ *zip.FileHeader) []byte {
			if n == "OEBPS/content.opf" {
				return bytes.Replace(b, []byte(">2026-01-02T03:04:05Z<"), []byte(">\n 2026-01-02T03:04:05Z\n<"), 1)
			}
			return b
		}},
		{"unlisted file", "OPF-010", func(n string, b []byte, _ *zip.FileHeader) []byte { return b }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := rezip(t, data, c.edit)
			if c.code == "OPF-010" {
				// Append a stray file the manifest does not mention.
				bad = rezip(t, bad, func(n string, b []byte, _ *zip.FileHeader) []byte { return b })
				var out bytes.Buffer
				zr, _ := zip.NewReader(bytes.NewReader(bad), int64(len(bad)))
				zw := zip.NewWriter(&out)
				for _, f := range zr.File {
					body, _ := readZip(f)
					w, _ := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method})
					w.Write(body)
				}
				w, _ := zw.Create("OEBPS/stray.txt")
				w.Write([]byte("x"))
				zw.Close()
				bad = out.Bytes()
			}
			probs := validatePackage(bad, want)
			if !strings.Contains(codes(probs), c.code) {
				t.Errorf("expected %s, got [%s]", c.code, codes(probs))
			}
		})
	}
}

func TestEscapesTitle(t *testing.T) {
	data, _ := goodBook(t)
	zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	for _, f := range zr.File {
		if f.Name == "OEBPS/content.opf" {
			b, _ := readZip(f)
			if !bytes.Contains(b, []byte("Test &lt;&amp;&gt; &#34;Book&#34;")) {
				t.Errorf("title not escaped:\n%s", b)
			}
		}
	}
}
