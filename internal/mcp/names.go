package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// maxNameLen and invalidNameChars follow the OpenAI function-name rule ^[a-zA-Z0-9_-]{1,64}$.
const maxNameLen = 64

var invalidNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// modelName returns "<server>__<tool>", hash-suffixed when sanitized or truncated so names stay unique.
func modelName(server, tool string) string {
	raw := server + "__" + tool
	name := invalidNameChars.ReplaceAllString(raw, "_")
	if name == raw && len(name) <= maxNameLen {
		return name
	}
	suffix := "_" + shortHash(server, tool)
	if len(name) > maxNameLen-len(suffix) {
		name = name[:maxNameLen-len(suffix)]
	}
	return name + suffix
}

func shortHash(server, tool string) string {
	sum := sha256.Sum256([]byte(server + "\x00" + tool))
	return hex.EncodeToString(sum[:4])
}
