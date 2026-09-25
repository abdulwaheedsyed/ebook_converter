package main

import (
	"fmt"
	"io"
	"os"

	"github.com/abdulwaheedsyed/leafbind/internal/check"
)

// runCheck validates EPUBs with the built-in checks and prints the findings
// the way epubcheck does. It exits 1 when any book has an error.
func runCheck(files []string, stdout, stderr io.Writer) int {
	code := 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			code = 1
			continue
		}
		var n [check.Fatal + 1]int
		for _, m := range check.EPUB(data) {
			if m.Severity < check.Warning {
				continue
			}
			n[m.Severity]++
			fmt.Fprintf(stdout, "%s: %s\n", f, m)
		}
		fmt.Fprintf(stdout, "%s: Messages: %d fatals / %d errors / %d warnings\n", f, n[check.Fatal], n[check.Error], n[check.Warning])
		if n[check.Fatal]+n[check.Error] > 0 {
			code = 1
		}
	}
	return code
}
