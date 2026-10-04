// Package manifest describes the contents of one backup archive.
package manifest

import "time"

const FileName = "manifest.json"

type Manifest struct {
	App         string     `json:"app"`
	CreatedAt   time.Time  `json:"created_at"`
	Tag         string     `json:"tag,omitempty"` // e.g. "prerestore"
	ToolVersion string     `json:"tool_version"`
	Artifacts   []Artifact `json:"artifacts"`
}

// Artifact is what one source produced inside the archive.
type Artifact struct {
	Source string            `json:"source"` // source name, also its directory in the archive
	Kind   string            `json:"kind"`
	Host   string            `json:"host"`
	Files  map[string]string `json:"files"` // archive-relative path -> sha256
	Meta   map[string]any    `json:"meta,omitempty"`
}
