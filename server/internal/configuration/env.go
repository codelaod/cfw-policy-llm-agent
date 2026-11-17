package configuration

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxFileBytes = 64 << 10

// Errors identify only a fixed field name or line number, never input values.
func invalidField(key string) error { return fmt.Errorf("CONFIG_INVALID: %s", key) }

func readEnv(path string, known map[string]field) (map[string]string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CONFIG_FILE_UNAVAILABLE")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("CONFIG_FILE_UNAVAILABLE")
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("CONFIG_FILE_UNAVAILABLE")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil || len(body) > maxFileBytes {
		return nil, fmt.Errorf("CONFIG_FILE_TOO_LARGE_OR_UNREADABLE")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), maxFileBytes+1)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		_, recognized := known[key]
		_, duplicate := values[key]
		if !found || !recognized || duplicate {
			return nil, fmt.Errorf("CONFIG_FILE_INVALID_LINE: %d", lineNumber)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("CONFIG_FILE_INVALID_LINE: %d", lineNumber)
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("CONFIG_FILE_INVALID_LINE: %d", lineNumber)
			}
			value = value[1:end]
		} else {
			// A leading # or one following whitespace starts a comment.
			if strings.HasPrefix(value, "#") {
				value = ""
			}
			for i := 1; i < len(value); i++ {
				if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
					value = strings.TrimSpace(value[:i])
					break
				}
			}
		}
		if strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return nil, fmt.Errorf("CONFIG_FILE_INVALID_LINE: %d", lineNumber)
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("CONFIG_FILE_UNREADABLE")
	}
	return values, nil
}
