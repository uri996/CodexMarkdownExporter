package main

import (
	"os"
	"strings"
)

func rolloutFormat(path string) string {
	if strings.HasSuffix(path, ".jsonl.zst") {
		return "zstd"
	}
	return "jsonl"
}

// Format conversion alone keeps the V1 key. If both physical representations
// coexist with different contents, reserve a stable secondary slot for zstd.
// The slot is path/format based, not content based: a growing source still skips.
func batchSourceIdentity(s sessionSummary, digest string, index *batchIndex) (string, bool) {
	base := sessionSourceKey(s)
	secondary := fingerprint(base + "\x00zstd-variant")
	compressed := rolloutFormat(s.Path) == "zstd"
	key := base
	if compressed {
		if _, ok := index.Entries[secondary]; ok {
			return secondary, false
		}
	}
	sibling := s.Path + ".zst"
	if compressed {
		sibling = strings.TrimSuffix(s.Path, ".zst")
	}
	if _, err := os.Stat(sibling); os.IsNotExist(err) {
		return key, false
	}
	other, err := fileFingerprint(sibling)
	if err == nil && other == digest {
		return key, false
	}
	// An unreadable sibling is conservatively treated as distinct; its own
	// iteration reports the read failure without hiding the readable version.
	changed := false
	if entry, ok := index.Entries[base]; ok && entry.SourceFormat == "zstd" {
		if _, occupied := index.Entries[secondary]; !occupied {
			index.Entries[secondary] = entry
			delete(index.Entries, base)
			changed = true
		}
	}
	if compressed {
		key = secondary
	}
	return key, changed
}
