package puredb

import "strings"

// LikeCmp tests whether string s matches the SQL LIKE pattern r.
// In the pattern:
//   - '%' matches any sequence of zero or more characters
//   - '_' matches exactly one character
//   - All other characters match literally (case-sensitive)
//
// Returns true if s matches the pattern.
func LikeCmp(s, r string) bool {
	return likeCmp(s, r)
}

// ILikeCmp tests whether string s matches the SQL LIKE pattern r,
// case-insensitively using Unicode case folding.
func ILikeCmp(s, r string) bool {
	s1 := strings.ToLower(s)
	r1 := strings.ToLower(r)
	return likeCmp(s1, r1)
}

func likeCmp(s, r string) bool {
	if len(r) == 0 {
		return len(s) == 0
	}

	switch r[0] {
	case '_':
		// Match exactly one character
		if len(s) == 0 {
			return false
		}
		return likeCmp(s[1:], r[1:])

	case '%':
		// Match any number of characters, including zero
		// Try matching at each position in s, including the end
		for i := 0; i <= len(s); i++ {
			if likeCmp(s[i:], r[1:]) {
				return true
			}
		}
		return false

	default:
		// Match literal characters up to the next wildcard
		i := 0
		for i < len(r) && r[i] != '_' && r[i] != '%' {
			i++
		}
		if len(s) < i {
			return false
		}
		if s[:i] != r[:i] {
			return false
		}
		return likeCmp(s[i:], r[i:])
	}
}
