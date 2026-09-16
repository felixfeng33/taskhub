// Package skills bundles the same taskhub skill shipped in the repository.
package skills

import "embed"

// Files contains the portable skill and its Codex UI metadata.
//
//go:embed taskhub/SKILL.md taskhub/agents/openai.yaml
var Files embed.FS
