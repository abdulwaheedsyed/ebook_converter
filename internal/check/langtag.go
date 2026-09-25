package check

import "strings"

// LanguageTagError reports why tag is not a well-formed BCP 47 language tag,
// or "" when it is.
func LanguageTagError(tag string) string { return langTagError(tag) }

// langTagError reports why tag is not a well-formed BCP 47 language tag
// (RFC 5646, section 2.1), or "" when it is. This is well-formedness only:
// subtags are not checked against the registry, as epubcheck does not.
func langTagError(tag string) string {
	if tag == "" {
		return "the tag is empty"
	}
	if grandfathered[strings.ToLower(tag)] {
		return ""
	}
	parts := strings.Split(tag, "-")
	for _, p := range parts {
		if p == "" {
			return "empty subtag"
		}
		if len(p) > 8 || !alnum(p) {
			return "invalid subtag \"" + p + "\""
		}
	}
	i := 0
	// A private-use tag on its own.
	if strings.EqualFold(parts[0], "x") {
		return privateUse(parts)
	}
	// language: 2-3 letters with up to three extlangs, or 4, or 5-8 letters.
	l := parts[0]
	if !alpha(l) || len(l) < 2 {
		return "invalid language subtag \"" + l + "\""
	}
	i = 1
	if len(l) <= 3 {
		for n := 0; n < 3 && i < len(parts) && len(parts[i]) == 3 && alpha(parts[i]); n++ {
			i++
		}
	}
	// script
	if i < len(parts) && len(parts[i]) == 4 && alpha(parts[i]) {
		i++
	}
	// region
	if i < len(parts) && ((len(parts[i]) == 2 && alpha(parts[i])) || (len(parts[i]) == 3 && digits(parts[i]))) {
		i++
	}
	// variants
	for i < len(parts) {
		p := parts[i]
		if (len(p) >= 5) || (len(p) == 4 && p[0] >= '0' && p[0] <= '9') {
			i++
			continue
		}
		break
	}
	// extensions
	seen := map[string]bool{}
	for i < len(parts) && len(parts[i]) == 1 && !strings.EqualFold(parts[i], "x") {
		s := strings.ToLower(parts[i])
		if seen[s] {
			return "duplicate extension singleton \"" + s + "\""
		}
		seen[s] = true
		i++
		n := 0
		for i < len(parts) && len(parts[i]) >= 2 {
			i++
			n++
		}
		if n == 0 {
			return "extension \"" + s + "\" has no subtags"
		}
	}
	if i < len(parts) && strings.EqualFold(parts[i], "x") {
		return privateUse(parts[i:])
	}
	if i < len(parts) {
		return "unexpected subtag \"" + parts[i] + "\""
	}
	return ""
}

func privateUse(parts []string) string {
	if len(parts) < 2 {
		return "private use subtag \"x\" has no subtags"
	}
	return ""
}

func alpha(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

func digits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func alnum(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// Grandfathered tags from RFC 5646, which do not follow the grammar.
var grandfathered = map[string]bool{
	"en-gb-oed": true, "i-ami": true, "i-bnn": true, "i-default": true, "i-enochian": true,
	"i-hak": true, "i-klingon": true, "i-lux": true, "i-mingo": true, "i-navajo": true,
	"i-pwn": true, "i-tao": true, "i-tay": true, "i-tsu": true, "sgn-be-fr": true,
	"sgn-be-nl": true, "sgn-ch-de": true, "art-lojban": true, "cel-gaulish": true,
	"no-bok": true, "no-nyn": true, "zh-guoyu": true, "zh-hakka": true, "zh-min": true,
	"zh-min-nan": true, "zh-xiang": true,
}
