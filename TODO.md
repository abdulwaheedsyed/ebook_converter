# To do

## Remove the remaining external tools

The converter itself needs nothing installed. These are the places where a
complete workflow still reaches outside the binary.

- [ ] **Validation equivalent to epubcheck, built in.** Today the converter
  runs its own structural checks — ZIP layout, well-formed XML, package
  metadata, manifest and spine, page images — and runs
  [epubcheck](https://github.com/w3c/epubcheck) only when it is installed,
  which needs Java. Port the epubcheck rules that apply to fixed-layout
  EPUB 3.3 into Go: the package document schema and its Schematron rules,
  XHTML content document rules, the navigation document, CSS, media types,
  OCF container rules, and the fixed-layout `rendition:*` rules. Report
  epubcheck's own message codes (PKG-, OPF-, RSC-, HTM-, CSS-, NAV-) so
  results can be compared line for line. Go has no RELAX NG validator, so
  the schemas either need one written or the rules they express
  hand-translated.
  - [ ] Build a corpus of valid and deliberately broken EPUBs and test that
    the built-in checks agree with epubcheck on every one.
  - [ ] Until parity, run epubcheck in CI, where Java is available, so it
    keeps checking every build's output.
- [ ] **A native window for the GUI, instead of the browser.** The interface
  opens in an installed Chrome, Edge or Chromium as an app window, or in the
  default browser. A native window needs the system web view — WebView2 on
  Windows, WKWebView on macOS, WebKitGTK on Linux — and the usual Go bindings
  need cgo, which would end the single static, cross-compiled binary. Look at
  loading those libraries at run time without cgo (for example with
  [purego](https://github.com/ebitengine/purego)): straightforward on
  Windows and macOS, where the web view is part of the system; on Linux,
  WebKitGTK is not always installed, so keep the browser as a fallback.
- [ ] **A page preview, to lessen the need for Kindle Previewer.** Amazon's
  Kindle Previewer is proprietary and cannot be built in, but its main use
  here — checking that pages look right before sending a book to a Kindle —
  can be: a flip-through of the finished EPUB in the GUI, at a Kindle-sized
  viewport, would catch most problems without it.
- [ ] **PDF inspection is built in; keep it that way.** Deciding a scan's
  resolution used to mean running poppler's `pdfimages` and `pdftotext` by
  hand. `--dpi auto` now does it. Any future diagnostics should follow suit.

## Platform integration

- [ ] Windows: an icon and version information in the `.exe`, from a
  resource file generated in Go so no Windows toolchain is needed.
- [ ] macOS: ship an `.app` bundle beside the command-line binary, so
  double-clicking opens the GUI without a Terminal window.
- [ ] Linux: a `.desktop` file and icon in the release archives.
- [ ] Code signing for Windows and notarisation for macOS, so downloaded
  binaries open without security warnings.

## Features

- [ ] `--password` for encrypted PDFs.
- [ ] Converting a page range.
- [ ] A table of contents from the PDF's outline, where it has one.
