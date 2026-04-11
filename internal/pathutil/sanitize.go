package pathutil

import (
	"path/filepath"
	"strings"
	"unicode"
)

var replacer = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"@", "_",
	"?", "_",
	"*", "_",
	"<", "_",
	">", "_",
	"|", "_",
)

// stripControlChars removes null bytes and non-printable control characters
// from s, providing defense-in-depth against path injection attacks.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0 || unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func SanitizeSegment(seg string) string {
	seg = strings.TrimSpace(seg)
	seg = stripControlChars(seg)
	if seg == "" {
		return "unknown"
	}
	seg = replacer.Replace(seg)
	seg = strings.ReplaceAll(seg, "..", "_")
	seg = strings.ReplaceAll(seg, string(filepath.Separator), "_")
	return seg
}

func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = stripControlChars(name)
	if name == "" {
		return "file"
	}
	name = replacer.Replace(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	return name
}
