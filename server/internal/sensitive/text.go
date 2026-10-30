package sensitive

import (
	"regexp"
	"sort"
	"strings"
)

var shape = regexp.MustCompile(`(?i)(?:\b(?:LTAI[A-Za-z0-9]{8,}|(?:AKIA|ASIA)[A-Z0-9]{16}|sk-[A-Za-z0-9_-]{12,})\b|(?:access[_ -]?key(?:[_ -]?(?:id|secret))?|secret(?:[_ -]?access[_ -]?key)?|security[_ -]?token|api[_ -]?key|password|密码|密钥)\s*[:=：]\s*["']?[^\s，。；"']{4,})`)

func Recognizable(value string) bool { return shape.MatchString(value) }

// Redact filters recognizable values without promising arbitrary secret detection.
func Redact(value string, known []string) (string, bool) {
	before := value
	values := append([]string(nil), known...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, secret := range values {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[敏感内容已移除]")
		}
	}
	value = shape.ReplaceAllString(value, "[敏感内容已移除]")
	return value, value != before
}
