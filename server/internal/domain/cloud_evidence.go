package domain

import (
	"encoding/base64"
	"encoding/json"
)

const MaxCloudEvidenceBytes = 32 << 20

// CloudRawArtifact is kept in the private evidence store. JSON encodes the
// body as base64 to preserve the exact bytes and their SHA256.
type CloudRawArtifact struct {
	Round     int               `json:"round"`
	Operation string            `json:"operation"`
	Query     map[string]string `json:"query"`
	Body      []byte            `json:"body"`
	SHA256    string            `json:"sha256"`
}

// EncodedSize measures the JSON entry without allocating a base64 copy.
func (a CloudRawArtifact) EncodedSize() int {
	bodySize := base64.StdEncoding.EncodedLen(len(a.Body))
	a.Body = a.Body[:0]
	metadata, _ := json.Marshal(a)
	return len(metadata) + bodySize
}
