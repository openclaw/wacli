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
	"\x00", "", // strip null bytes
)

// stripControl removes ASCII control characters (except tab and newline)
// and the DEL character, which can cause issues on various filesystems.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return '_'
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func SanitizeSegment(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return "unknown"
	}
	seg = stripControl(seg)
	seg = replacer.Replace(seg)
	seg = strings.ReplaceAll(seg, "..", "_")
	seg = strings.ReplaceAll(seg, string(filepath.Separator), "_")
	return seg
}

func SanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "file"
	}
	name = stripControl(name)
	name = replacer.Replace(name)
	name = strings.ReplaceAll(name, "..", "_")
	name = strings.ReplaceAll(name, string(filepath.Separator), "_")
	return name
}
