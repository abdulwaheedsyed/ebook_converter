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
	if o.DPI != 180 || o.MaxEdge != 2560 || o.Quality != 92 || o.Direction != "ltr" || o.Lang != "en" || !o.Validate || !o.Epubcheck {
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
