package check

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDumpCorpus writes the corpus to $CORPUS_DIR, for inspection by hand.
func TestDumpCorpus(t *testing.T) {
	dir := os.Getenv("CORPUS_DIR")
	if dir == "" {
		t.Skip("set CORPUS_DIR to write the corpus")
	}
	for _, c := range corpus() {
		if err := os.WriteFile(filepath.Join(dir, c.name+".epub"), c.data(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
