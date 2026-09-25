package check

import (
	"bytes"
	"embed"
	"io/fs"
	"sync"

	"github.com/abdulwaheedsyed/leafbind/internal/rng"
)

// The XHTML content document schema, as EPUBCheck 5.4.0 ships it: EPUB's
// own modules over the validator.nu HTML schema, with MathML, SVG and ITS.
// The files are verbatim, each under the license in its directory.
//
//go:embed schema
var schemaFiles embed.FS

var xhtmlSchema = sync.OnceValues(func() (*rng.Schema, error) {
	sub, err := fs.Sub(schemaFiles, "schema/xhtml")
	if err != nil {
		return nil, err
	}
	return rng.Load(sub, "epub-xhtml-30.rnc")
})

// checkSchema validates a content document against the XHTML schema and
// reports what it finds as RSC-005, as EPUBCheck does.
func (c *checker) checkSchema(it *item, data []byte) {
	s, err := xhtmlSchema()
	if err != nil {
		return // cannot happen with the embedded schema; the tests load it
	}
	errs, perr := s.Validate(bytes.NewReader(data))
	if perr != nil {
		return // not well-formed: already reported as RSC-016
	}
	for _, e := range errs {
		c.report("RSC-005", it.path, e.Line, e.Column, e.Message)
	}
}
