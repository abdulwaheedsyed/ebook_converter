package rng

import "testing"

func TestXSDRegexp(t *testing.T) {
	cases := []struct {
		pattern string
		yes, no []string
	}{
		{`#[a-fA-F0-9]{6}`, []string{"#00ff9A"}, []string{"#00ff9", "x#00ff9A", "#00ff9Az"}},
		{`[\i-[:]][\c-[:]]*`, []string{"abc", "a-b.c", "_x1"}, []string{"a:b", ":a", "1a"}},
		{`(([\i-[:]][\c-[:]]*)?:)[^\s]*`, []string{"dc:title", ":x"}, []string{"a b:c"}},
		// The compact syntax's lexer resolves \x{0A} to a newline first.
		{"[ \n-\r]*", []string{" \n\r"}, []string{"x"}},
		{`$^`, []string{"$^"}, []string{""}}, // ^ and $ are ordinary in XSD
		{`[0-9]+\*`, []string{"12*"}, []string{"12"}},
		{`[a-z-]+`, []string{"a-b"}, []string{"A"}},
	}
	for _, c := range cases {
		re, err := xsdRegexp(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		for _, s := range c.yes {
			if !re.MatchString(s) {
				t.Errorf("%q should match %q", c.pattern, s)
			}
		}
		for _, s := range c.no {
			if re.MatchString(s) {
				t.Errorf("%q should not match %q", c.pattern, s)
			}
		}
	}
}
