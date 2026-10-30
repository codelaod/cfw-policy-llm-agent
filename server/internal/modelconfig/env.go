package modelconfig

import (
	"bufio"
	"bytes"
	"os"
	"strconv"
	"strings"
)

var modelEnvKeys = []string{"CFW_MODEL_BASE_URL", "CFW_MODEL_API_KEY", "CFW_MODEL_NAME", "CFW_MODEL_API_STYLE", "CFW_MODEL_TIMEOUT_SECONDS", "CFW_MODEL_MAX_CALLS", "CFW_MODEL_MAX_OUTPUT_TOKENS"}

// LoadEnvFile reads only this explicit file and its seven process overrides.
// Compatibility limits must be stated; it does not create cumulative policy.
func LoadEnvFile(path string) (Config, error) { return loadEnvFile(path, os.LookupEnv) }
func loadEnvFile(path string, lookup envLookup) (Config, error) {
	body, err := readFile(path, 64<<10)
	if err != nil {
		return Config{}, err
	}
	values := map[string]string{}
	known := map[string]bool{}
	for _, key := range modelEnvKeys {
		known[key] = true
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	first := true
	for scanner.Scan() {
		line := scanner.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envName.MatchString(key) {
			return Config{}, ErrInvalid
		}
		if !known[key] {
			if strings.HasPrefix(strings.ToUpper(key), "CFW_MODEL_") {
				return Config{}, ErrInvalid
			}
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return Config{}, ErrInvalid
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return Config{}, ErrInvalid
			}
			value = value[1 : len(value)-1]
		}
		if key != "CFW_MODEL_API_KEY" && hasControl(value) {
			return Config{}, ErrInvalid
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return Config{}, ErrInvalid
	}
	for _, key := range modelEnvKeys {
		if value, exists := lookup(key); exists {
			values[key] = value
		}
	}
	numbers := map[string]int{}
	for _, key := range []string{"CFW_MODEL_TIMEOUT_SECONDS", "CFW_MODEL_MAX_CALLS", "CFW_MODEL_MAX_OUTPUT_TOKENS"} {
		value, exists := values[key]
		if !exists || value == "" || strings.TrimSpace(value) != value {
			return Config{}, ErrInvalid
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, ErrInvalid
		}
		numbers[key] = n
	}
	input := fileConfig{APIStyle: values["CFW_MODEL_API_STYLE"], ProviderProfile: "openai-compatible", BaseURL: values["CFW_MODEL_BASE_URL"], Model: values["CFW_MODEL_NAME"], APIKeyEnv: "CFW_MODEL_API_KEY", MaxOutputTokens: numbers["CFW_MODEL_MAX_OUTPUT_TOKENS"], TimeoutSeconds: numbers["CFW_MODEL_TIMEOUT_SECONDS"], MaxHTTPAttempts: numbers["CFW_MODEL_MAX_CALLS"], Enabled: true}
	c, err := build(input, "env_file")
	if err != nil {
		return Config{}, err
	}
	c.setCredential(values["CFW_MODEL_API_KEY"])
	return c, nil
}
