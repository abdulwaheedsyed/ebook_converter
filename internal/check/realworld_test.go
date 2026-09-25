package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

// TestRealWorld compares the built-in checks with epubcheck on a folder of
// EPUBs from anywhere, such as the W3C/IDPF epub3-samples releases:
//
//	REALWORLD=/path/to/epubs go test -run TestRealWorld -v ./internal/check
//
// It needs epubcheck on PATH. It fails on false positives, findings epubcheck
// does not make, and logs what the built-in checks miss.
func TestRealWorld(t *testing.T) {
	dir := os.Getenv("REALWORLD")
	if dir == "" {
		t.Skip("set REALWORLD to a folder of EPUBs")
	}
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Fatal("epubcheck is not on PATH")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.epub"))
	sort.Strings(files)
	agree := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		want := epubcheck(t, bin, "book", data)
		got := builtin(data)
		inWant, inGot := map[finding]bool{}, map[finding]bool{}
		for _, x := range want {
			inWant[x] = true
		}
		for _, x := range got {
			inGot[x] = true
		}
		var extra, missing []finding
		for _, x := range got {
			if !inWant[x] {
				extra = append(extra, x)
			}
		}
		for _, x := range want {
			if !inGot[x] {
				missing = append(missing, x)
			}
		}
		name := filepath.Base(f)
		switch {
		case len(extra) > 0:
			t.Errorf("%s: false positives %v", name, extra)
		case len(missing) > 0:
			t.Logf("%s: missed %v", name, missing)
		default:
			agree++
		}
	}
	t.Logf("built-in checks agree with epubcheck on %d of %d books", agree, len(files))
}
