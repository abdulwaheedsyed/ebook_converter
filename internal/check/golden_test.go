package check

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

const golden = "testdata/epubcheck.json"

// finding is the part of a message both checkers must agree on.
type finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
}

func (f finding) String() string { return f.Severity + " " + f.ID + " " + f.Path }

// normalise keeps one finding per ID, severity and file, sorted. Usage and
// informational messages are left out: epubcheck only prints them on request.
func normalise(fs []finding) []finding {
	seen := map[finding]bool{}
	var out []finding
	for _, f := range fs {
		if f.Severity == "USAGE" || f.Severity == "INFO" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

func builtin(data []byte) []finding {
	var fs []finding
	for _, m := range EPUB(data) {
		fs = append(fs, finding{m.ID, m.Severity.String(), m.Path})
	}
	return normalise(fs)
}

// epubcheck runs the real epubcheck on one EPUB.
func epubcheck(t *testing.T, bin, name string, data []byte) []finding {
	dir := t.TempDir()
	in, out := filepath.Join(dir, name+".epub"), filepath.Join(dir, "out.json")
	os.WriteFile(in, data, 0o644)
	exec.Command(bin, in, "--json", out).Run() // exits non-zero when it finds errors
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("%s: epubcheck wrote no report", name)
	}
	var rep struct {
		Messages []struct {
			ID        string `json:"ID"`
			Severity  string `json:"severity"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var fs []finding
	for _, m := range rep.Messages {
		for _, l := range m.Locations {
			p := l.Path
			if strings.HasSuffix(p, ".epub") { // the container as a whole
				p = ""
			}
			fs = append(fs, finding{m.ID, m.Severity, p})
		}
	}
	return normalise(fs)
}

func loadGolden(t *testing.T) map[string][]finding {
	t.Helper()
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; record it with EPUBCHECK_UPDATE=1", err)
	}
	var g map[string][]finding
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// TestEpubcheckGolden records epubcheck's findings for the corpus
// (EPUBCHECK_UPDATE=1) or checks that the recording still matches a live
// epubcheck (EPUBCHECK_VERIFY=1). Otherwise it is skipped.
func TestEpubcheckGolden(t *testing.T) {
	update, verify := os.Getenv("EPUBCHECK_UPDATE") != "", os.Getenv("EPUBCHECK_VERIFY") != ""
	if !update && !verify {
		t.Skip("set EPUBCHECK_UPDATE or EPUBCHECK_VERIFY to run epubcheck")
	}
	bin, err := exec.LookPath("epubcheck")
	if err != nil {
		t.Fatal("epubcheck is not on PATH")
	}
	cases := corpus()
	got := make([][]finding, len(cases))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range cases {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			got[i] = epubcheck(t, bin, c.name, c.data())
		}()
	}
	wg.Wait()

	live := map[string][]finding{}
	for i, c := range cases {
		live[c.name] = got[i]
		if live[c.name] == nil {
			live[c.name] = []finding{}
		}
	}
	if update {
		b, _ := json.MarshalIndent(live, "", "  ")
		if err := os.WriteFile(golden, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d cases in %s", len(live), golden)
		return
	}
	want := loadGolden(t)
	for name, fs := range live {
		if !slices.Equal(fs, want[name]) {
			t.Errorf("%s: epubcheck now reports %v, the recording says %v", name, fs, want[name])
		}
	}
}

// TestMatchesEpubcheck is the point of the package: on every case of the
// corpus, the built-in checks report what epubcheck reports.
func TestMatchesEpubcheck(t *testing.T) {
	want := loadGolden(t)
	cases := corpus()
	if len(want) != len(cases) {
		t.Errorf("the recording has %d cases, the corpus %d; rerun with EPUBCHECK_UPDATE=1", len(want), len(cases))
	}
	agree := 0
	for _, c := range cases {
		got := builtin(c.data())
		w, ok := want[c.name]
		if !ok {
			t.Errorf("%s: not in the recording", c.name)
			continue
		}
		if slices.Equal(got, w) {
			agree++
			continue
		}
		t.Errorf("%s:\n  epubcheck: %v\n  built-in:  %v", c.name, w, got)
	}
	t.Logf("built-in checks agree with epubcheck on %d of %d cases", agree, len(cases))
}

var _ = fmt.Sprint
