# ebook_converter

Converts a PDF into a Kindle-compatible **fixed-layout EPUB 3**. Every page
becomes an image on one shared canvas, so the original typesetting survives
exactly — right-to-left scripts, complex ligatures, tables and slide layouts
included. It is built for scanned books and slide decks, where reflowing the
text is not an option.

It is a single static executable with no runtime dependencies: no poppler, no
ImageMagick, no zip tool, no Java.

## Features

- **Orientation detected per book.** Landscape decks come out landscape and
  portrait books portrait, including pages that use the PDF `/Rotate` flag.
- **No stretching, no split pages.** Pages of differing sizes are scaled to fit
  one canvas and letterboxed, never distorted.
- **E-ink options.** 8-bit greyscale, and flattening of tinted page backgrounds
  to white for better contrast.
- **Right-to-left or left-to-right** page progression.
- **Validates its own output**, and runs [epubcheck](https://github.com/w3c/epubcheck)
  as well when it is installed.
- **Compact.** A built-in JPEG encoder builds Huffman tables for each page
  from its own statistics — lossless, and typically 5–10% smaller than
  Go's standard encoder.
- **Fast.** Pages are rendered in parallel.
- **Cross-platform.** Linux, macOS and Windows, on x86-64 and ARM64.

## Install

Download the archive for your platform from the
[releases page](https://github.com/abdulwaheedsyed/ebook_converter/releases),
check it against `SHA256SUMS`, and put the `ebook_converter` binary somewhere
on your `PATH`:

```bash
sha256sum --ignore-missing -c SHA256SUMS      # macOS: shasum -a 256 -c SHA256SUMS
tar -xzf ebook_converter-*-linux-amd64.tar.gz
```

On macOS, a binary downloaded through a browser is quarantined by Gatekeeper
because it is not notarised. Clear the flag once after extracting:

```bash
xattr -d com.apple.quarantine ebook_converter
```

Or install from source with Go 1.27 or later:

```bash
go install github.com/abdulwaheedsyed/ebook_converter@latest
```

Or build from a clone:

```bash
make build        # ./ebook_converter for this machine
make dist         # every supported platform, into dist/
make package      # release archives and SHA256SUMS, into dist/
```

No C compiler is needed for any target: the build is pure Go with
`CGO_ENABLED=0`.

## Usage

```bash
ebook_converter [options] input.pdf output.epub
```

Options may appear anywhere on the command line, as `--name value`,
`--name=value` or `-name value`.

| Option | Default | Meaning |
| --- | --- | --- |
| `--dpi N` | `180` | Render resolution. See [Choosing a DPI](#choosing-a-dpi). |
| `--max-edge N` | `2560` | Cap on the longest canvas edge in pixels; `0` disables the cap. |
| `--quality N` | `92` | JPEG quality, 1–100. |
| `--grayscale` | off | 8-bit greyscale for e-ink. Aliases: `--greyscale`, `--mono`. |
| `--flatten-bg` | off | Force a flat, tinted page background to white. Aliases: `--white-bg`, `--flatten-background`. |
| `--title TEXT` | file name | Book title. |
| `--lang CODE` | `en` | BCP 47 language tag, such as `en`, `ar`, `ur` or `ur-Latn`. |
| `--rtl` | LTR | Right-to-left page progression, for Arabic, Urdu, Hebrew and similar. `--ltr` selects the default explicitly. |
| `--orientation X` | detected | Force `portrait`, `landscape`, `auto` or `none`. |
| `--mixed` | off | Keep each page's own canvas instead of one shared canvas. |
| `--jobs N` | CPUs, max 6 | Pages rendered in parallel. |
| `--no-validate` | off | Skip all validation. |
| `--no-epubcheck` | off | Skip the external epubcheck even when it is installed. |
| `-v`, `--verbose` | off | Print one line per page. |
| `--version` | | Print the version. |
| `--licenses` | | Print the license and third-party notices. |

Exit status is `0` on success, `1` when conversion or validation fails, and
`2` for a usage error.

## Examples

A book in greyscale for e-ink:

```bash
ebook_converter --grayscale --title "Book Title" book.pdf book.epub
```

A right-to-left book, such as Arabic or Urdu:

```bash
ebook_converter --grayscale --rtl --lang ar --title "Book Title" book.pdf book.epub
```

A book printed on a tinted background:

```bash
ebook_converter --grayscale --flatten-bg book.pdf book.epub
```

A scanned book, rendered at the scan's native resolution:

```bash
ebook_converter --grayscale --dpi 150 scan.pdf scan.epub
```

A smaller file, at some cost in sharpness:

```bash
ebook_converter --grayscale --dpi 150 --quality 85 --max-edge 1920 book.pdf book.epub
```

Every PDF in a directory (POSIX shell):

```bash
for f in *.pdf; do ebook_converter --grayscale "$f" "${f%.pdf}.epub"; done
```

The same in PowerShell:

```powershell
Get-ChildItem *.pdf | ForEach-Object { ebook_converter --grayscale $_.FullName ($_.BaseName + ".epub") }
```

## Choosing a DPI

The default of 180 DPI suits a PDF whose pages are vector text, where
rendering at a higher resolution resolves more detail.

A scanned PDF is different: each page is an image with a fixed resolution, and
rendering above it only interpolates. That makes a larger file with no more
detail, which the reader then rescales anyway. It is better to ship the scan's
native pixels and let the device scale once.

To tell which kind a PDF is, with poppler's tools where available:

```bash
pdftotext -f 1 -l 5 book.pdf - | wc -w   # no words: the pages are images
pdfimages -list -f 1 -l 5 book.pdf       # a scan: shows each image's ppi
```

If the pages are single images, pass their ppi as `--dpi`.

## How it works

1. **Measure.** Each page's pixel size at `--dpi` is taken from the PDF
   engine, including any `/Rotate`, so a rotated page is never mistaken for the
   wrong orientation.
2. **Pick a canvas.** The canvas starts from the most common page size, grows
   if needed to contain the largest page at that same aspect ratio, is capped
   at `--max-edge`, and is rounded down to even dimensions.
3. **Render and fit.** Pages are rendered in parallel, scaled to fit inside
   the canvas with their aspect ratio intact, and padded out to its exact size.
   Nothing is cropped or stretched. The padding colour is sampled from the edge
   being padded, so letterbox bars blend with the page.
4. **Convert.** Optionally greyscale and background flattening.
5. **Encode.** Each page is written as a baseline JPEG with Huffman tables
   optimised for that page.
6. **Package.** Standard EPUB 3 fixed-layout metadata, plus Kindle's own
   fixed-layout metadata. Page 1 becomes the cover.
7. **Validate** the package that was written.

### Why one canvas

Kindle uses a single canvas per book. When pages declare viewports with
differing aspect ratios, the ones that do not match are distorted to fit.
Normalising every page onto one canvas prevents that. `--mixed` keeps
per-page canvases for a book that genuinely mixes portrait and landscape
pages, but Kindle handles such books poorly; splitting the PDF into separate
books usually gives a better result.

### Greyscale

E-ink Kindles display about 16 levels of grey, so colour only adds bytes there.
The conversion is Rec. 709 luma on the gamma-encoded values, which preserves
perceived lightness. Converting in linear light instead drags mid tones much
darker, which costs legibility on a low-contrast panel.

Colour Kindles and the Kindle apps do display colour, and greyscale is lossy
for them. Keep the source PDFs.

### Background flattening

A tint across most of the page costs contrast an e-ink panel cannot spare.
`--flatten-bg` detects each page's dominant colour and whitens it only when it
covers at least a quarter of the page and is light enough to be a background.
In greyscale this is a white point, which leaves every darker tone unchanged,
so text is not touched. In colour it matches the background colour itself, so
the hue of a tint is never shifted. Dark themes are left alone, since removing
that background would mean inverting the page.

## Validation

Every conversion is checked before the program reports success:

- the ZIP layout: `mimetype` first, stored uncompressed, with no extra field;
- every XML and XHTML file is well-formed;
- the package metadata, including `rendition:layout` being exactly
  `pre-paginated`, a valid BCP 47 language and a correctly formatted
  modification date;
- every manifest entry exists, every file is listed, and there is exactly one
  navigation document and one cover image;
- one spine entry per PDF page;
- every page image decodes, matches its page's viewport, and, in normalised
  mode, matches the canvas.

When `epubcheck` is on `PATH` it runs as well, as an independent check. It is
optional: without it the built-in validation still runs.

A book that fails validation is still written, so it can be inspected, but
the program exits with status `1`. The report groups problems by code, since
one fault usually repeats on every page.

## Supported platforms

| OS | Architectures |
| --- | --- |
| Linux | amd64, arm64 |
| macOS | amd64, arm64 |
| Windows | amd64, arm64 |

These are the architectures where the embedded WebAssembly runtime compiles to
native code. On others it falls back to an interpreter and becomes far too slow
to be useful.

## Dependencies

There is no PDF rasteriser in Go's standard library, so rendering uses
[go-pdfium](https://github.com/klippa-app/go-pdfium): Google's PDFium, compiled
to WebAssembly and run inside the process by
[wazero](https://github.com/tetratelabs/wazero), a pure-Go WebAssembly
runtime. This is what keeps the binary free of cgo and system libraries.

The PDF engine runs sandboxed with no filesystem access; the PDF is passed to
it in memory. JPEG encoding is the program's own. Everything else — colour
conversion, ZIP packaging, XML and validation — is Go's standard library, plus
[golang.org/x/image](https://pkg.go.dev/golang.org/x/image) for resampling.

## Development

```bash
make test         # all tests, including end-to-end conversions (about 20 s)
make test-short   # unit tests only; skips anything that starts the PDF engine
make notices      # regenerate THIRD_PARTY_NOTICES.md after changing dependencies
```

CI runs the full test suite on every released platform for each push to
`main` and each pull request. Pushing a tag such as `v1.2.3` builds, tests
and publishes a release with the archives attached.

The tests fail if a linked module is missing from `THIRD_PARTY_NOTICES.md` or
listed at the wrong version, so the notices cannot silently go stale.

The end-to-end tests generate their own PDFs, so no sample documents are
needed. They cover landscape and portrait detection, `/Rotate`, pages of
differing sizes, `--mixed`, greyscale with background flattening, and invalid
input. The validator is tested against deliberately broken packages. The
JPEG encoder is tested for lossless optimisation, valid length-limited code
tables, minimal padding blocks, and quality against `image/jpeg`.

## Notes

- **Whitespace in the package document is significant.** `rendition:*` values
  are compared as exact strings. Written across several lines,
  `pre-paginated` becomes `"\n  pre-paginated\n"`, which matches nothing, and
  the book silently falls back to a reflowable layout. The validator checks
  for this.
- **Output is written atomically.** The EPUB is written to a temporary file
  beside the destination and renamed into place, so an interrupted run never
  leaves a half-written book or destroys an existing one.
- **JPEG encoding.** Go's `image/jpeg` always uses the example Huffman tables
  from the JPEG specification. This program's encoder instead gathers each
  page's symbol statistics and builds length-limited optimal tables, the
  procedure of Annex K.2 of the specification and what libjpeg's
  `optimize_coding` does. Output is baseline JPEG for the widest reader
  support: one component for greyscale, YCbCr with 4:2:0 subsampling for
  colour. The optimisation is lossless — the tests check that decoded pixels
  are identical to an encoding with the standard tables — and `jpegtran
  -optimize` finds nothing further to remove.

## License

ebook_converter is released under the [MIT License](LICENSE).

Its executables include third-party software under their own licenses — Go,
the Go modules it uses, and PDFium with the libraries compiled into its
WebAssembly build. [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists them
with their full license texts. The same text is embedded in every binary and
printed by `ebook_converter --licenses`, and `make dist` places both files
beside the binaries it builds.

This software is based in part on the work of the FreeType Team.

This software is based in part on the work of the Independent JPEG Group.
