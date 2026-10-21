package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// Configuration loading does not initialize a provider or perform network I/O.
const maxEnvFileBytes = 64 * 1024

var modelConfigKeys = []string{
	"CFW_MODEL_BASE_URL", "CFW_MODEL_API_KEY", "CFW_MODEL_NAME", "CFW_MODEL_API_STYLE",
	"CFW_MODEL_TIMEOUT_SECONDS", "CFW_MODEL_MAX_CALLS", "CFW_MODEL_MAX_OUTPUT_TOKENS",
}

type envLookup func(string) (string, bool)

type modelConfig struct {
	baseURL         string
	apiKey          string
	modelName       string
	apiStyle        string
	timeoutSeconds  int
	maxCalls        int
	maxOutputTokens int
}

type modelConfigSummary struct {
	BaseURLConfigured bool   `json:"base_url_configured"`
	APIKeyConfigured  bool   `json:"api_key_configured"`
	ModelConfigured   bool   `json:"model_configured"`
	APIStyle          string `json:"api_style"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
	MaxModelCalls     int    `json:"max_model_calls"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
}

func (c modelConfig) summary() modelConfigSummary {
	return modelConfigSummary{c.baseURL != "", c.apiKey != "", c.modelName != "",
		c.apiStyle, c.timeoutSeconds, c.maxCalls, c.maxOutputTokens}
}

// Even accidental formatting/JSON logging must not reveal credentials or endpoint details.
func (c modelConfig) MarshalJSON() ([]byte, error) { return json.Marshal(c.summary()) }
func (c modelConfig) String() string               { b, _ := c.MarshalJSON(); return string(b) }
func (c modelConfig) GoString() string             { return c.String() }

func knownModelConfigKey(key string) bool {
	for _, candidate := range modelConfigKeys {
		if key == candidate {
			return true
		}
	}
	return false
}

func parseModelEnv(reader io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxEnvFileBytes)
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
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvKey(key) {
			return nil, fmt.Errorf("模型配置文件第 %d 行不是合法的 KEY=VALUE", lineNumber)
		}
		if !knownModelConfigKey(key) {
			if strings.HasPrefix(key, "CFW_MODEL_") {
				return nil, fmt.Errorf("模型配置文件第 %d 行包含未知模型变量，请检查拼写", lineNumber)
			}
			continue // Other services may share the file, but their credentials are not loaded.
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("模型配置文件第 %d 行重复定义模型变量", lineNumber)
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return nil, fmt.Errorf("模型配置文件第 %d 行引号未闭合", lineNumber)
			}
			value = value[1 : len(value)-1]
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("模型配置文件第 %d 行包含不允许的控制字符", lineNumber)
		}
		values[key] = value // Literal data: no shell execution or variable expansion.
	}
	if scanner.Err() != nil {
		return nil, errors.New("模型配置文件行过长或读取失败")
	}
	return values, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, ch := range key {
		if ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' {
			continue
		}
		if i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func loadModelConfig(path string, lookup envLookup) (modelConfig, error) {
	values := make(map[string]string)
	if path != "" {
		file, err := os.Open(path)
		if err == nil {
			defer file.Close()
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() > maxEnvFileBytes {
				return modelConfig{}, errors.New("模型配置文件必须是最多 64 KiB 的普通文件")
			}
			values, err = parseModelEnv(io.LimitReader(file, maxEnvFileBytes+1))
			if err != nil {
				return modelConfig{}, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return modelConfig{}, errors.New("无法读取模型配置文件，请检查文件与权限")
		}
	}
	// Explicit process environment values override the file, including empty values.
	for _, key := range modelConfigKeys {
		if value, present := lookup(key); present {
			values[key] = value
		}
	}
	for _, key := range []string{"CFW_MODEL_BASE_URL", "CFW_MODEL_API_KEY", "CFW_MODEL_NAME"} {
		if strings.TrimSpace(values[key]) == "" {
			return modelConfig{}, fmt.Errorf("%s 未填写", key)
		}
	}
	endpoint, err := validateModelBaseURL(values["CFW_MODEL_BASE_URL"])
	if err != nil {
		return modelConfig{}, err
	}
	if len(values["CFW_MODEL_API_KEY"]) > 8192 || strings.IndexFunc(values["CFW_MODEL_API_KEY"], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return modelConfig{}, errors.New("CFW_MODEL_API_KEY 格式无效，不允许空白、控制字符或过长内容")
	}
	name := strings.TrimSpace(values["CFW_MODEL_NAME"])
	if len(name) > 256 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return modelConfig{}, errors.New("CFW_MODEL_NAME 格式无效")
	}
	style := values["CFW_MODEL_API_STYLE"]
	if style == "" {
		style = "chat_completions"
	}
	if style != "chat_completions" && style != "responses" {
		return modelConfig{}, errors.New("CFW_MODEL_API_STYLE 只能是 chat_completions 或 responses；这只是配置标记，不代表协议已适配")
	}
	timeout, err := boundedConfigInt(values, "CFW_MODEL_TIMEOUT_SECONDS", 30, 1, 120)
	if err != nil {
		return modelConfig{}, err
	}
	calls, err := boundedConfigInt(values, "CFW_MODEL_MAX_CALLS", 4, 1, 8)
	if err != nil {
		return modelConfig{}, err
	}
	tokens, err := boundedConfigInt(values, "CFW_MODEL_MAX_OUTPUT_TOKENS", 512, 16, 4096)
	if err != nil {
		return modelConfig{}, err
	}
	return modelConfig{baseURL: endpoint, apiKey: values["CFW_MODEL_API_KEY"], modelName: name,
		apiStyle: style, timeoutSeconds: timeout, maxCalls: calls, maxOutputTokens: tokens}, nil
}

func validateModelBaseURL(value string) (string, error) {
	fail := func() (string, error) {
		return "", errors.New("CFW_MODEL_BASE_URL 无效：使用 HTTPS Base URL（本机可用 HTTP），不能含账号密码、查询参数或片段")
	}
	if len(value) > 2048 || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fail()
	}
	u, err := url.Parse(value)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fail()
	}
	if strings.EqualFold(u.Hostname(), "your-model-provider.example") {
		return "", errors.New("CFW_MODEL_BASE_URL 仍是示例地址，请替换为实际服务的 Base URL")
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" {
			return fail()
		}
		host := u.Hostname()
		address, parseErr := netip.ParseAddr(host)
		if !strings.EqualFold(host, "localhost") && (parseErr != nil || !address.IsLoopback()) {
			return fail()
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	path := strings.ToLower(u.Path)
	if strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/responses") {
		return "", errors.New("CFW_MODEL_BASE_URL 应填写服务商的 Base URL，而非完整 chat/completions 或 responses 请求地址")
	}
	return u.String(), nil
}

func boundedConfigInt(values map[string]string, key string, fallback, minimum, maximum int) (int, error) {
	value, present := values[key]
	if !present || value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < minimum || number > maximum {
		return 0, fmt.Errorf("%s 必须在 %d 到 %d 之间", key, minimum, maximum)
	}
	return number, nil
}

func checkModelConfiguration(path string, lookup envLookup, out, errOut io.Writer) int {
	config, err := loadModelConfig(path, lookup)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	result := struct {
		Mode        string `json:"mode"`
		ConfigValid bool   `json:"config_valid"`
		RequestSent bool   `json:"request_sent"`
		modelConfigSummary
	}{Mode: "model_config_check", ConfigValid: true, RequestSent: false, modelConfigSummary: config.summary()}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if encoder.Encode(result) != nil {
		fmt.Fprintln(errOut, "无法输出脱敏配置检查结果")
		return 1
	}
	return 0
}
