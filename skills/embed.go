// Package skills embeds the agent skill shipped with the wacli binary.
package skills

import "embed"

//go:embed wacli/SKILL.md
var FS embed.FS
