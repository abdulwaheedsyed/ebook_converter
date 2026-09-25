package check

import (
	"strings"
	"testing"
)

// TestSchematron covers the Schematron rules the corpus does not reach,
// each with markup that breaks it and markup that must pass.
func TestSchematron(t *testing.T) {
	cases := []struct {
		body string
		want string // a fragment of the message, or "" for none
	}{
		{`<p id="a">x</p><p id="a">y</p>`, `Duplicate ID "a"`},
		{`<p id="a">x</p><p aria-labelledby="a">y</p>`, ""},
		{`<p aria-labelledby="a b">y</p><p id="a">x</p>`, "aria-labelledby attribute must refer"},
		{`<map name="m" id="n"><area href="#x" alt="x"/></map>`, "same value as the name attribute"},
		{`<map name="m"><area href="#x" alt="x"/></map>`, ""},
		{`<p lang="en" xml:lang="EN">x</p>`, ""},
		{`<p lang="en" xml:lang="fr">x</p>`, "lang and xml:lang"},
		{`<p><label for="i">x</label><input id="i"/></p>`, ""},
		{`<p><label for="i">x</label><input id="i" type="hidden"/></p>`, "allowed target element"},
		{`<table><tr><th id="h">h</th></tr><tr><td headers="h">d</td></tr></table>`, ""},
		{`<table><tr><td id="h">h</td></tr><tr><td headers="h">d</td></tr></table>`, "th elements in the same table"},
		{`<div aria-activedescendant="c"><p id="c">x</p></div>`, ""},
		{`<p id="c">x</p><div aria-activedescendant="c">y</div>`, "must refer to a descendant"},
		{`<p><a href="#x"><img src="i.png" alt="" ismap="ismap"/></a></p>`, ""},
		{`<p><img src="i.png" alt="" ismap="ismap"/></p>`, "ancestor a[@href]"},
		{`<p><a href="#x"><input type="hidden"/></a></p>`, ""},
		{`<p><a href="#x"><input/></a></p>`, "input element must not appear inside a"},
		{`<address><footer>f</footer></address>`, "footer element must not appear inside address"},
		{`<select><option selected="selected">a</option><option selected="selected">b</option></select>`, "more than one descendant option"},
		{`<select multiple="multiple"><option selected="selected">a</option><option selected="selected">b</option></select>`, ""},
		{`<p><input list="d"/><span id="d">x</span></p>`, "expecting: datalist"},
		{`<script src="s.js" type="text/plain"></script>`, "JavaScript MIME Type"},
		{`<script src="s.js" type="module"></script>`, ""},
		{`<p role="doc-endnote">x</p>`, `"doc-endnote" role is deprecated`},
		{`<div role="doc-pagefooter" aria-label="x">f</div>`, "'aria-label' attribute must not be specified"},
		{`<p itemprop="x"><a itemprop="y">z</a></p>`, "href attribute must also be specified"},
		{`<math xmlns="http://www.w3.org/1998/Math/MathML" xref="nope"><mi>x</mi></math>`, `the ID "nope" does not exist`},
	}
	for _, tc := range cases {
		doc := `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">` +
			`<head><title>t</title></head><body>` + tc.body + `</body></html>`
		n, perr := parseXML([]byte(doc))
		if perr != nil {
			t.Fatalf("%s: %s", tc.body, perr.msg)
		}
		c := &checker{}
		c.checkSchematron(&item{path: "p.xhtml"}, n.root())
		var got []string
		for _, m := range c.msgs {
			got = append(got, m.Text)
		}
		all := strings.Join(got, "\n")
		switch {
		case tc.want == "" && len(got) > 0:
			t.Errorf("%s: unexpected %q", tc.body, all)
		case tc.want != "" && !strings.Contains(all, tc.want):
			t.Errorf("%s: want %q, got %q", tc.body, tc.want, all)
		}
	}
}
