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

func SanitizeSegment(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "unknown"
	}
	seg = replacer.Replace(seg)
	seg = stripControlChars(seg)
	seg = strings.ReplaceAll(seg, "..", "_")
	seg = strings.ReplaceAll(seg, string(filepath.Separator), "_")
	return seg
}

func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	name = replacer.Replace(name)
	name = stripControlChars(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	return name
}
