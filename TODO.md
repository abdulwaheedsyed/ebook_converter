# To do

## Remove the remaining external tools

The converter itself needs nothing installed. These are the places where a
complete workflow still reaches outside the binary.

- [ ] **Validation equivalent to epubcheck, built in.** Started:
  `internal/check` reports EPUBCheck's own codes, severities and texts, and
  `leafbind --check` runs it on any EPUB.
  - [x] OCF container: `mimetype`, file names, `container.xml`, several
    renditions.
  - [x] Package documents: required metadata, language tags, modification
    date, `rendition:*` properties, prefixes, manifest properties and
    fallbacks, spine.
  - [x] Content and navigation documents: well-formedness, deprecated
    elements, references, fixed-layout viewports, declared features
    (`switch`, `mathml`, `svg`, `scripted`).
  - [x] HTML content models: which elements and attributes may appear
    where. `internal/rng` is a RELAX NG validator, written for this, that
    runs EPUBCheck's own XHTML schema, embedded unchanged; the rules the
    schema cannot express (forbidden descendants, ID references and the
    like, from EPUBCheck's Schematron) are written out in Go.
  - [x] CSS syntax errors; image formats and corruption.
  - [x] A corpus of 86 books, each broken in one way, with EPUBCheck 5.4.0's
    findings recorded; the built-in checks agree on every case, and CI
    re-runs EPUBCheck so the recording cannot drift.
  - [x] Agreement on the W3C EPUB 3 samples, with no false positives
    (`REALWORLD=dir go test -run TestRealWorld ./internal/check`).
  - [ ] The navigation document's own schema rules, and package documents
    against their schema, instead of the hand-written checks; `internal/rng`
    can load EPUBCheck's schemas for both.
  - [ ] The EPUB CSS profile's rules beyond syntax (`CSS-001` and on),
    fonts, and font obfuscation (`encryption.xml`).
  - [ ] Remote resources and the `remote-resources` property, media overlays,
    `epub:type` vocabularies, SVG content documents, and EPUB 2 packages.
  - [ ] Once these are covered, stop running an installed epubcheck by
    default.
- [ ] **A native window for the GUI, instead of the browser.** The interface
  opens in an installed Chrome, Edge or Chromium as an app window, or in the
  default browser. A native window needs the system web view — WebView2 on
  Windows, WKWebView on macOS, WebKitGTK on Linux — and the usual Go bindings
  need cgo, which would end the single static, cross-compiled binary. Look at
  loading those libraries at run time without cgo (for example with
  [purego](https://github.com/ebitengine/purego)): straightforward on
  Windows and macOS, where the web view is part of the system; on Linux,
  WebKitGTK is not always installed, so keep the browser as a fallback.
- [x] **A page preview, to lessen the need for Kindle Previewer.** Amazon's
  Kindle Previewer is proprietary and cannot be built in, but its main use
  here can be: checking that pages look right before sending a book to a
  Kindle. The GUI flips through the finished EPUB on a Kindle-sized screen.
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
- [x] A table of contents from the PDF's outline, where it has one, and a
  page list from its page labels.
