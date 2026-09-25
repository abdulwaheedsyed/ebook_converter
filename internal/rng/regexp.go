package rng

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// XML name characters, for XSD's \i (name start) and \c (name) escapes.
var (
	nameStartRanges = [][2]rune{
		{':', ':'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}, {0xC0, 0xD6}, {0xD8, 0xF6}, {0xF8, 0x2FF},
		{0x370, 0x37D}, {0x37F, 0x1FFF}, {0x200C, 0x200D}, {0x2070, 0x218F}, {0x2C00, 0x2FEF},
		{0x3001, 0xD7FF}, {0xF900, 0xFDCF}, {0xFDF0, 0xFFFD}, {0x10000, 0xEFFFF},
	}
	nameExtraRanges = [][2]rune{{'-', '-'}, {'.', '.'}, {'0', '9'}, {0xB7, 0xB7}, {0x300, 0x36F}, {0x203F, 0x2040}}
)

// xsdRegexp translates an XML Schema regular expression into Go's syntax.
// XSD patterns match the whole value, treat ^ and $ as ordinary characters,
// and add the \i and \c classes and class subtraction, [a-z-[aeiou]].
func xsdRegexp(src string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString(`^(?:`)
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '[':
			ranges, neg, n, err := parseClass(src[i:])
			if err != nil {
				return nil, err
			}
			b.WriteString(classString(ranges, neg))
			i += n
		case c == '\\' && i+1 < len(src):
			switch e := src[i+1]; e {
			case 'i':
				b.WriteString(classString(nameStartRanges, false))
			case 'I':
				b.WriteString(classString(nameStartRanges, true))
			case 'c':
				b.WriteString(classString(append(append([][2]rune{}, nameStartRanges...), nameExtraRanges...), false))
			case 'C':
				b.WriteString(classString(append(append([][2]rune{}, nameStartRanges...), nameExtraRanges...), true))
			default:
				b.WriteString(src[i : i+2])
			}
			i += 2
		case c == '^' || c == '$':
			b.WriteByte('\\')
			b.WriteByte(c)
			i++
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			b.WriteString(src[i : i+size])
			i += size
		}
	}
	b.WriteString(`)$`)
	return regexp.Compile(b.String())
}

// parseClass reads a character class, including a subtraction, and returns
// its ranges, whether it is negated, and how many bytes it spans.
func parseClass(s string) ([][2]rune, bool, int, error) {
	i := 1
	neg := false
	if i < len(s) && s[i] == '^' {
		neg = true
		i++
	}
	var ranges [][2]rune
	for i < len(s) && s[i] != ']' {
		if s[i] == '-' && i+1 < len(s) && s[i+1] == '[' { // subtraction
			sub, subNeg, n, err := parseClass(s[i+1:])
			if err != nil {
				return nil, false, 0, err
			}
			if subNeg {
				return nil, false, 0, fmt.Errorf("negated subtraction is not supported")
			}
			ranges = subtract(ranges, sub)
			i += 1 + n
			continue
		}
		lo, n, set, err := classAtom(s[i:])
		if err != nil {
			return nil, false, 0, err
		}
		i += n
		if set != nil {
			ranges = append(ranges, set...)
			continue
		}
		hi := lo
		if i+1 < len(s) && s[i] == '-' && s[i+1] != ']' && s[i+1] != '[' {
			h, m, hset, err := classAtom(s[i+1:])
			if err != nil || hset != nil {
				return nil, false, 0, fmt.Errorf("bad range")
			}
			hi = h
			i += 1 + m
		}
		ranges = append(ranges, [2]rune{lo, hi})
	}
	if i >= len(s) {
		return nil, false, 0, fmt.Errorf("unterminated class")
	}
	return ranges, neg, i + 1, nil
}

// classAtom reads one character, or an escape that stands for a set.
func classAtom(s string) (rune, int, [][2]rune, error) {
	if s[0] == '\\' && len(s) > 1 {
		switch e := s[1]; e {
		case 'i':
			return 0, 2, nameStartRanges, nil
		case 'c':
			return 0, 2, append(append([][2]rune{}, nameStartRanges...), nameExtraRanges...), nil
		case 'd':
			return 0, 2, [][2]rune{{'0', '9'}}, nil
		case 's':
			return 0, 2, [][2]rune{{' ', ' '}, {'\t', '\t'}, {'\n', '\n'}, {'\r', '\r'}}, nil
		case 'n':
			return '\n', 2, nil, nil
		case 'r':
			return '\r', 2, nil, nil
		case 't':
			return '\t', 2, nil, nil
		case 'S', 'D', 'w', 'W', 'I', 'C', 'p', 'P':
			return 0, 0, nil, fmt.Errorf("escape \\%c in a class is not supported", e)
		default:
			r, size := utf8.DecodeRuneInString(s[1:])
			return r, 1 + size, nil, nil
		}
	}
	r, size := utf8.DecodeRuneInString(s)
	return r, size, nil, nil
}

func subtract(ranges, sub [][2]rune) [][2]rune {
	out := ranges
	for _, s := range sub {
		var next [][2]rune
		for _, r := range out {
			if s[1] < r[0] || s[0] > r[1] {
				next = append(next, r)
				continue
			}
			if r[0] < s[0] {
				next = append(next, [2]rune{r[0], s[0] - 1})
			}
			if r[1] > s[1] {
				next = append(next, [2]rune{s[1] + 1, r[1]})
			}
		}
		out = next
	}
	return out
}

// classChar writes one character for use inside a Go character class.
func classChar(r rune) string {
	if strings.ContainsRune(`\]^-[`, r) {
		return `\` + string(r)
	}
	if r < 0x20 || r > 0x7e {
		return fmt.Sprintf(`\x{%X}`, r)
	}
	return string(r)
}

func classString(ranges [][2]rune, neg bool) string {
	var b strings.Builder
	b.WriteByte('[')
	if neg {
		b.WriteByte('^')
	}
	for _, r := range ranges {
		b.WriteString(classChar(r[0]))
		if r[1] != r[0] {
			b.WriteByte('-')
			b.WriteString(classChar(r[1]))
		}
	}
	if len(ranges) == 0 && !neg {
		return `[^\x00-\x{10FFFF}]` // matches nothing
	}
	b.WriteByte(']')
	return b.String()
}
