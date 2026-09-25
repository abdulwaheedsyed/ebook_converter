package rng

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type tokKind int

const (
	tEOF     tokKind = iota
	tIdent           // NCName, possibly a keyword, or an escaped identifier
	tCName           // prefix:local
	tNsName          // prefix:*
	tLiteral         // quoted string
	tOp              // = |= &= , | & ? * + - ( ) { } [ ] ~
)

type token struct {
	kind    tokKind
	text    string // identifier, name or literal value, or operator
	escaped bool   // \identifier: never a keyword
	line    int
}

// Private-use stand-ins for escaped line breaks; see newLexer.
const (
	escapedLF = '\uE000'
	escapedCR = '\uE001'
)

var unescapeBreaks = strings.NewReplacer(string(escapedLF), "\n", string(escapedCR), "\r")

type lexer struct {
	src  []rune
	pos  int
	line int
	file string
}

// newLexer prepares the source, replacing \x{HHHH} escapes, which the
// compact syntax allows anywhere and resolves before tokenising.
func newLexer(file, src string) (*lexer, error) {
	var b strings.Builder
	for i := 0; i < len(src); i++ {
		if src[i] == '\\' && i+2 < len(src) && src[i+1] == 'x' {
			j := i + 1
			for j < len(src) && src[j] == 'x' {
				j++
			}
			if j < len(src) && src[j] == '{' {
				end := strings.IndexByte(src[j:], '}')
				if end > 0 {
					n, err := strconv.ParseUint(src[j+1:j+end], 16, 32)
					if err != nil {
						return nil, fmt.Errorf("%s: bad \\x escape", file)
					}
					// A raw line break may not appear in a literal, but an
					// escaped one may; keep it distinguishable until then.
					switch r := rune(n); r {
					case '\n':
						b.WriteRune(escapedLF)
					case '\r':
						b.WriteRune(escapedCR)
					default:
						b.WriteRune(r)
					}
					i = j + end
					continue
				}
			}
		}
		b.WriteByte(src[i])
	}
	return &lexer{src: []rune(b.String()), line: 1, file: file}, nil
}

func isNameStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isNameChar(r rune) bool {
	return isNameStart(r) || r == '-' || r == '.' || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || r == 0xB7
}

func (l *lexer) errorf(format string, args ...any) error {
	return fmt.Errorf("%s:%d: %s", l.file, l.line, fmt.Sprintf(format, args...))
}

func (l *lexer) next() (token, error) {
	// Skip white space and comments (# to end of line, including ## ones).
	for l.pos < len(l.src) {
		r := l.src[l.pos]
		if r == '\n' {
			l.line++
			l.pos++
		} else if unicode.IsSpace(r) {
			l.pos++
		} else if r == '#' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		} else {
			break
		}
	}
	if l.pos >= len(l.src) {
		return token{kind: tEOF, line: l.line}, nil
	}
	r := l.src[l.pos]
	line := l.line

	switch {
	case r == '"' || r == '\'':
		return l.literal()
	case r == '\\':
		l.pos++
		id := l.name()
		if id == "" {
			return token{}, l.errorf("bad escaped identifier")
		}
		return token{kind: tIdent, text: id, escaped: true, line: line}, nil
	case isNameStart(r):
		id := l.name()
		if l.pos < len(l.src) && l.src[l.pos] == ':' {
			if l.pos+1 < len(l.src) && l.src[l.pos+1] == '*' {
				l.pos += 2
				return token{kind: tNsName, text: id, line: line}, nil
			}
			if l.pos+1 < len(l.src) && isNameStart(l.src[l.pos+1]) {
				l.pos++
				local := l.name()
				return token{kind: tCName, text: id + ":" + local, line: line}, nil
			}
		}
		return token{kind: tIdent, text: id, line: line}, nil
	}
	for _, op := range []string{"|=", "&=", "=", ",", "|", "&", "?", "*", "+", "-", "(", ")", "{", "}", "[", "]", "~"} {
		if strings.HasPrefix(string(l.src[l.pos:min(len(l.src), l.pos+2)]), op) {
			l.pos += len(op)
			return token{kind: tOp, text: op, line: line}, nil
		}
	}
	return token{}, l.errorf("unexpected character %q", r)
}

func (l *lexer) name() string {
	start := l.pos
	if l.pos < len(l.src) && isNameStart(l.src[l.pos]) {
		l.pos++
		for l.pos < len(l.src) && isNameChar(l.src[l.pos]) {
			l.pos++
		}
	}
	return string(l.src[start:l.pos])
}

func (l *lexer) literal() (token, error) {
	line := l.line
	q := l.src[l.pos]
	triple := l.pos+2 < len(l.src) && l.src[l.pos+1] == q && l.src[l.pos+2] == q
	if triple {
		l.pos += 3
		start := l.pos
		for l.pos+2 < len(l.src) && !(l.src[l.pos] == q && l.src[l.pos+1] == q && l.src[l.pos+2] == q) {
			if l.src[l.pos] == '\n' {
				l.line++
			}
			l.pos++
		}
		s := unescapeBreaks.Replace(string(l.src[start:l.pos]))
		l.pos += 3
		return token{kind: tLiteral, text: s, line: line}, nil
	}
	l.pos++
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != q {
		if l.src[l.pos] == '\n' {
			return token{}, l.errorf("newline in literal")
		}
		l.pos++
	}
	if l.pos >= len(l.src) {
		return token{}, l.errorf("unterminated literal")
	}
	s := unescapeBreaks.Replace(string(l.src[start:l.pos]))
	l.pos++
	return token{kind: tLiteral, text: s, line: line}, nil
}
