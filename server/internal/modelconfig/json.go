package modelconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// uniqueJSON rejects keys that could overwrite the same configuration field.
func uniqueJSON(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	var visit func(int) bool
	visit = func(depth int) bool {
		if depth > 20 {
			return false
		}
		t, e := d.Token()
		if e != nil {
			return false
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return true
		}
		if delim == '{' {
			seen := []string{}
			for d.More() {
				k, e := d.Token()
				s, ok := k.(string)
				if e != nil || !ok {
					return false
				}
				// encoding/json matches struct fields with Unicode case folding.
				for _, previous := range seen {
					if strings.EqualFold(previous, s) {
						return false
					}
				}
				seen = append(seen, s)
				if !visit(depth + 1) {
					return false
				}
			}
		} else if delim == '[' {
			for d.More() {
				if !visit(depth + 1) {
					return false
				}
			}
		} else {
			return false
		}
		_, e = d.Token()
		return e == nil
	}
	if !visit(0) {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}
