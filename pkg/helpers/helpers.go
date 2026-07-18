// Package helpers
package helpers

import (
	"strings"
)

// ParseImageRef parses an image reference string into it's docker url foramt chunks
// The `ref` parameter can accept a tagged reference or digested reference
//   - tagged ref: "ubuntu:latest" (uses ':' to identify a mutable version)
//   - digest ref: "ubuntu@sha256:45xx" (uses '@' to lock to an immutable version with <algorithm>:<hash>)
//
// Returns registry, repo and reference
func ParseImageRef(ref string) (registry, repo, reference string) {
	registry = "registry-1.docker.io"
	reference = "latest"

	if strings.Contains(ref, "@") {
		parts := strings.SplitN(ref, "@", 2)
		ref = parts[0]
		reference = parts[1]
	} else if strings.Contains(ref, ":") {
		parts := strings.SplitN(ref, ":", 2)
		ref = parts[0]
		reference = parts[1]
	}

	if !strings.Contains(ref, "/") {
		repo = "library/" + ref
	} else {
		repo = ref
	}

	return registry, repo, reference
}
