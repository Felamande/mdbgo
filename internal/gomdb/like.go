package gomdb

import "strings"

// LikeCmp tests whether string s matches the SQL LIKE pattern r.
// In the pattern:
//   - '%' matches any sequence of zero or more characters
//   - '_' matches exactly one character
//   - All other characters match literally (case-sensitive)
//
// Returns true if s matches the pattern.
func LikeCmp(s, r string) bool {
	return likeCmpBytes([]byte(s), []byte(r))
}

// ILikeCmp tests whether string s matches the SQL LIKE pattern r,
// case-insensitively using Unicode case folding.
func ILikeCmp(s, r string) bool {
	return iLikeCmpFolded(s, strings.ToLower(r))
}

func iLikeCmpFolded(s, foldedPattern string) bool {
	return likeCmpBytes([]byte(strings.ToLower(s)), []byte(foldedPattern))
}

// likeCmpBytes is the LIKE matcher over byte slices; UTF-8 bytes are
// compared as-is, preserving the historical byte-oriented semantics.
func likeCmpBytes(s, r []byte) bool {
	si, ri := 0, 0
	star := -1
	starMatch := 0

	for si < len(s) {
		if ri < len(r) && (r[ri] == '_' || r[ri] == s[si]) {
			si++
			ri++
			continue
		}
		if ri < len(r) && r[ri] == '%' {
			star = ri
			ri++
			starMatch = si
			continue
		}
		if star >= 0 {
			starMatch++
			si = starMatch
			ri = star + 1
			continue
		}
		return false
	}
	for ri < len(r) && r[ri] == '%' {
		ri++
	}
	return ri == len(r)
}
