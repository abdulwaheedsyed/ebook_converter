package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseArgsOptionsAnywhere(t *testing.T) {
	o, err := parseArgs([]string{"in.pdf", "--grayscale", "out.epub", "--dpi=150", "--lang", "ar", "--rtl", "-v"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Input != "in.pdf" || o.Output != "out.epub" {
		t.Errorf("positionals = %q %q", o.Input, o.Output)
	}
	if !o.Grayscale || o.DPI != 150 || o.Lang != "ar" || o.Direction != "rtl" || !o.Verbose {
		t.Errorf("options not applied: %+v", o)
	}
	if o, _ := parseArgs([]string{"a.pdf", "--toc=pages", "b.epub"}); o.TOC != tocPages {
		t.Errorf("options not applied: %+v", o)
	}
	if o.Title != "in" {
		t.Errorf("default title = %q, want the file name without extension", o.Title)
	}
}

func TestParseArgsAliases(t *testing.T) {
	for _, a := range []string{"--grayscale", "--greyscale", "--mono"} {
		if o, _ := parseArgs([]string{a, "a.pdf", "b.epub"}); !o.Grayscale {
			t.Errorf("%s did not enable greyscale", a)
		}
	}
	for _, a := range []string{"--flatten-bg", "--white-bg", "--flatten-background"} {
		if o, _ := parseArgs([]string{a, "a.pdf", "b.epub"}); !o.FlattenBG {
			t.Errorf("%s did not enable flattening", a)
		}
	}
}

func TestParseArgsDefaults(t *testing.T) {
	o, err := parseArgs([]string{"a.pdf", "b.epub"})
	if err != nil {
		t.Fatal(err)
	}
	if o.DPI != dpiAuto || o.MaxEdge != 2560 || o.Quality != 92 || o.Direction != "ltr" || o.Lang != "en" || o.TOC != tocBookmarks || !o.Validate || !o.Epubcheck {
		t.Errorf("unexpected defaults: %+v", o)
	}
}

func TestParseArgsErrors(t *testing.T) {
	cases := map[string][]string{
		"unknown option":      {"--nope", "a.pdf", "b.epub"},
		"missing value":       {"a.pdf", "b.epub", "--dpi"},
		"not a number":        {"--dpi", "high", "a.pdf", "b.epub"},
		"out of range":        {"--quality", "101", "a.pdf", "b.epub"},
		"bad orientation":     {"--orientation", "sideways", "a.pdf", "b.epub"},
		"bad contents":        {"--toc", "chapters", "a.pdf", "b.epub"},
		"bad language":        {"--lang", "this is not a language", "a.pdf", "b.epub"},
		"value on bool":       {"--grayscale=yes", "a.pdf", "b.epub"},
		"too few arguments":   {"a.pdf"},
		"too many arguments":  {"a.pdf", "b.epub", "c.epub"},
		"output is the input": {"a.pdf", "./a.pdf"},
	}
	for name, args := range cases {
		if _, err := parseArgs(args); err == nil || errors.Is(err, errHelp) {
			t.Errorf("%s: expected an error for %q", name, strings.Join(args, " "))
		}
	}
}

func TestParseArgsDPI(t *testing.T) {
	for arg, want := range map[string]int{"auto": dpiAuto, "150": 150, "300": 300} {
		o, err := parseArgs([]string{"--dpi", arg, "a.pdf", "b.epub"})
		if err != nil || o.DPI != want {
			t.Errorf("--dpi %s: got %d, %v; want %d", arg, o.DPI, err, want)
		}
	}
	for _, bad := range []string{"0", "17", "1201", "high"} {
		if _, err := parseArgs([]string{"--dpi", bad, "a.pdf", "b.epub"}); err == nil {
			t.Errorf("--dpi %s: expected an error", bad)
		}
	}
}

func TestGUIRequested(t *testing.T) {
	cases := []struct {
		args      []string
		gui, nobr bool
	}{
		{nil, true, false},
		{[]string{"-psn_0_12345"}, true, false},
		{[]string{"--gui"}, true, false},
		{[]string{"--gui", "--no-browser"}, true, true},
		{[]string{"--no-browser"}, true, true},
		{[]string{"a.pdf", "b.epub"}, false, false},
		{[]string{"--help"}, false, false},
		{[]string{"--gui", "a.pdf"}, false, false},
	}
	for _, c := range cases {
		g, ok := guiRequested(c.args)
		if ok != c.gui || g.NoBrowser != c.nobr {
			t.Errorf("guiRequested(%q) = %v, %+v; want gui=%v noBrowser=%v", c.args, ok, g, c.gui, c.nobr)
		}
	}
}

func TestParseArgsHelpAndVersion(t *testing.T) {
	if _, err := parseArgs([]string{"-h"}); !errors.Is(err, errHelp) {
		t.Errorf("-h: got %v", err)
	}
	if _, err := parseArgs([]string{"--version"}); !errors.Is(err, errVersion) {
		t.Errorf("--version: got %v", err)
	}
	if _, err := parseArgs([]string{"--licenses"}); !errors.Is(err, errLicenses) {
		t.Errorf("--licenses: got %v", err)
	}
}

func TestValidLang(t *testing.T) {
	for _, ok := range []string{"ur", "ar", "en", "ur-Latn", "en-GB", "zh-Hant-TW", "x-private"} {
		if !validLang(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "u", "this is not", "ur_PK", "-ur", "ur-"} {
		if validLang(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestResolveVersion(t *testing.T) {
	cases := []struct{ linked, module, want string }{
		{"v1.2.3", "v1.2.3", "v1.2.3"},  // release build
		{"v1.2.3", "(devel)", "v1.2.3"}, // release build from a checkout
		{"dev", "v1.2.3", "v1.2.3"},     // go install module@v1.2.3
		{"dev", "(devel)", "dev"},       // go build without version control
		{"dev", "", "dev"},
		{"", "v0.9.0", "v0.9.0"},
	}
	for _, c := range cases {
		if got := resolveVersion(c.linked, c.module); got != c.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", c.linked, c.module, got, c.want)
		}
	}
}
