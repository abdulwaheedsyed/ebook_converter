# Vendored license texts

These are license texts for components that are not pulled in as Go modules,
so there is no module directory to read them from: Go itself, and the
components compiled into the embedded PDFium WebAssembly module. They are copied verbatim.
`tools/gennotices` reads them to build `THIRD_PARTY_NOTICES.md`.

- `pdfium/`: the license bundle that ships with the PDFium WebAssembly
  build from [pdfium-binaries](https://github.com/bblanchon/pdfium-binaries),
  release `chromium/8066`. The module embedded by go-pdfium is built from a
  fork of that project with the same configuration: release, AGG renderer, no
  V8, no XFA.
- `go/`: Go's own license and patent grant, which cover the standard library
  and runtime in every Go binary, from [golang/go](https://github.com/golang/go).
  Some distributions move these files out of `GOROOT`, so they are not read
  from there.
- `epubcheck/`: the license of [EPUBCheck](https://github.com/w3c/epubcheck),
  whose message catalogue `internal/check` is generated from.
- `w3c/`: the W3C Software Notice and License (2002), which the MathML
  schema under `internal/check/schema/xhtml/mod/mathml` refers to but does
  not include, from [w3.org](https://www.w3.org/copyright/software-license-2002/).
  The other vendored schemas carry their licenses beside them.
- `emscripten/`: the license of the Emscripten toolchain, whose C runtime is
  linked into the module, and of the musl libc it bundles, from
  [emscripten-core/emscripten](https://github.com/emscripten-core/emscripten).

When go-pdfium updates its PDFium module, refresh `pdfium/` from the matching
pdfium-binaries release and rerun `make notices`.
