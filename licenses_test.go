package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"testing"
)

// noticeRows maps each component in the notices table to its version.
func noticeRows() map[string]string {
	rows := map[string]string{}
	re := regexp.MustCompile(`(?m)^\| \[([^\]]+)\]\(#[^)]*\) \| ([^|]+) \|`)
	for _, m := range re.FindAllStringSubmatch(noticesText, -1) {
		rows[m[1]] = strings.TrimSpace(m[2])
	}
	return rows
}

// Every module linked into the program must be in the notices, at the
// version actually linked. This fails after a dependency change until
// "make notices" is run.
func TestNoticesCoverLinkedModules(t *testing.T) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("no build information in this binary")
	}
	rows := noticeRows()
	for _, d := range bi.Deps {
		if d.Replace != nil {
			d = d.Replace
		}
		v, ok := rows[d.Path]
		switch {
		case !ok:
			t.Errorf("%s is linked but missing from THIRD_PARTY_NOTICES.md; run make notices", d.Path)
		case v != d.Version:
			t.Errorf("%s is linked at %s but the notices list %s; run make notices", d.Path, d.Version, v)
		}
	}
}

func TestNoticesIncludeRequiredCredits(t *testing.T) {
	for _, want := range []string{
		"This software is based in part on the work of the FreeType Team.",
		"This software is based in part on the work of the Independent JPEG Group.",
		"### PDFium", "### github.com/tetratelabs/wazero", "NOTICE:", // Apache-2.0 NOTICE files
		"### EPUBCheck", // its message catalogue is carried in internal/check
	} {
		if !strings.Contains(noticesText, want) {
			t.Errorf("THIRD_PARTY_NOTICES.md is missing %q", want)
		}
	}
	if !strings.HasPrefix(licenseText, "MIT License") {
		t.Error("LICENSE is not the MIT license")
	}
}

func TestVendoredLicensesExist(t *testing.T) {
	files, err := filepath.Glob("third_party/licenses/*/*")
	if err != nil || len(files) < 15 {
		t.Fatalf("expected the vendored license texts, found %d", len(files))
	}
	for _, f := range files {
		if st, err := os.Stat(f); err != nil || st.Size() == 0 {
			t.Errorf("%s is missing or empty", f)
		}
	}
}
