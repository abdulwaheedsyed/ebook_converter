package rng

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// datatype is a datatype with its parameters. Unknown datatypes accept every
// value, so they never produce a false error.
type datatype struct {
	lib, name string
	params    []param
	patterns  []*regexp.Regexp
	lenient   bool // unknown type or unusable pattern: accept anything
}

var (
	ncNameRE   = regexp.MustCompile(`^[\pL_][\pL\pN\pM._\-·]*$`)
	nmtokenRE  = regexp.MustCompile(`^[\pL\pN\pM._:\-·]+$`)
	integerRE  = regexp.MustCompile(`^[+-]?[0-9]+$`)
	decimalRE  = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)
	doubleRE   = regexp.MustCompile(`^([+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?|[+-]?INF|NaN)$`)
	languageRE = regexp.MustCompile(`^[a-zA-Z]{1,8}(-[a-zA-Z0-9]{1,8})*$`)
	dateRE     = regexp.MustCompile(`^-?[0-9]{4,}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])(Z|[+-][0-9]{2}:[0-9]{2})?$`)
	dateTimeRE = regexp.MustCompile(`^-?[0-9]{4,}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-4]):[0-5][0-9]:[0-5][0-9](\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})?$`)
)

var knownXSD = map[string]bool{
	"string": true, "normalizedString": true, "token": true, "anyURI": true, "NCName": true,
	"NMTOKEN": true, "NMTOKENS": true, "ID": true, "IDREF": true, "IDREFS": true, "language": true,
	"integer": true, "nonNegativeInteger": true, "positiveInteger": true, "unsignedLong": true,
	"decimal": true, "double": true, "float": true, "boolean": true, "date": true, "dateTime": true,
}

func newDatatype(lib, name string, params []param) *datatype {
	d := &datatype{lib: lib, name: name, params: params}
	switch {
	case lib == "" && (name == "string" || name == "token"):
	case lib == xsdLib && knownXSD[name]:
	default:
		d.lenient = true
	}
	for _, p := range params {
		if p.name != "pattern" {
			continue
		}
		re, err := xsdRegexp(p.value)
		if err != nil {
			d.lenient = true // an untranslatable pattern must not cause false errors
			continue
		}
		d.patterns = append(d.patterns, re)
	}
	return d
}

// normalise applies the type's white-space handling.
func (d *datatype) normalise(s string) string {
	if d.name == "string" && (d.lib == "" || d.lib == xsdLib) {
		return s
	}
	if d.name == "normalizedString" {
		return strings.Map(func(r rune) rune {
			if r == '\t' || r == '\n' || r == '\r' {
				return ' '
			}
			return r
		}, s)
	}
	return strings.Join(strings.Fields(s), " ")
}

// allows reports whether s is a valid value of the type.
func (d *datatype) allows(s string) bool {
	if d.lenient {
		return true
	}
	v := d.normalise(s)
	if !d.lexical(v) {
		return false
	}
	for _, re := range d.patterns {
		if !re.MatchString(v) {
			return false
		}
	}
	return d.facets(v)
}

func (d *datatype) lexical(v string) bool {
	if d.lib == "" {
		return true
	}
	switch d.name {
	case "NCName", "ID", "IDREF":
		return ncNameRE.MatchString(v)
	case "NMTOKEN":
		return nmtokenRE.MatchString(v)
	case "NMTOKENS":
		return v != "" && allFields(v, nmtokenRE)
	case "IDREFS":
		return v != "" && allFields(v, ncNameRE)
	case "language":
		return languageRE.MatchString(v)
	case "integer":
		return integerRE.MatchString(v)
	case "nonNegativeInteger", "unsignedLong":
		if !integerRE.MatchString(v) {
			return false
		}
		n, _ := new(big.Int).SetString(strings.TrimPrefix(v, "+"), 10)
		if n == nil || n.Sign() < 0 {
			return false
		}
		return d.name != "unsignedLong" || n.BitLen() <= 64
	case "positiveInteger":
		if !integerRE.MatchString(v) {
			return false
		}
		n, _ := new(big.Int).SetString(strings.TrimPrefix(v, "+"), 10)
		return n != nil && n.Sign() > 0
	case "decimal":
		return decimalRE.MatchString(v)
	case "double", "float":
		return doubleRE.MatchString(v)
	case "boolean":
		return v == "true" || v == "false" || v == "1" || v == "0"
	case "date":
		return dateRE.MatchString(v)
	case "dateTime":
		return dateTimeRE.MatchString(v)
	}
	return true // string, normalizedString, token, anyURI
}

func allFields(v string, re *regexp.Regexp) bool {
	for _, f := range strings.Fields(v) {
		if !re.MatchString(f) {
			return false
		}
	}
	return true
}

// facets checks the length and range parameters.
func (d *datatype) facets(v string) bool {
	num := func() (float64, bool) {
		f, err := strconv.ParseFloat(strings.TrimPrefix(v, "+"), 64)
		return f, err == nil && !math.IsNaN(f)
	}
	for _, p := range d.params {
		switch p.name {
		case "minLength", "maxLength", "length":
			n, err := strconv.Atoi(p.value)
			if err != nil {
				continue
			}
			l := len([]rune(v))
			if strings.HasSuffix(d.name, "S") { // list types count items
				l = len(strings.Fields(v))
			}
			if (p.name == "minLength" && l < n) || (p.name == "maxLength" && l > n) || (p.name == "length" && l != n) {
				return false
			}
		case "minInclusive", "maxInclusive", "minExclusive", "maxExclusive":
			lim, err := strconv.ParseFloat(p.value, 64)
			f, ok := num()
			if err != nil || !ok {
				continue
			}
			if (p.name == "minInclusive" && f < lim) || (p.name == "maxInclusive" && f > lim) ||
				(p.name == "minExclusive" && f <= lim) || (p.name == "maxExclusive" && f >= lim) {
				return false
			}
		}
	}
	return true
}

// equal reports whether s, as a value of the type, equals the pattern's
// value, which was normalised when the pattern was built.
func (d *datatype) equal(value, s string) bool {
	return d.normalise(s) == value
}
