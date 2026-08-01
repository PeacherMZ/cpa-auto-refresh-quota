package refreshquota

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"gopkg.in/yaml.v3"
)

const (
	defaultMaxTokens           = 16
	defaultDelayBetweenAuths   = time.Second
	defaultPrioritySyncTimeout = 5 * time.Second
	defaultRequestTimeout      = 2 * time.Minute
	maxMessageBytes            = 64 * 1024
	maxTimePoints              = 64
)

// StringList accepts either a YAML sequence or a comma-separated scalar.
type StringList []string

func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*s = nil
		return nil
	}
	switch node.Kind {
	case yaml.SequenceNode:
		out := make([]string, 0, len(node.Content))
		for _, item := range node.Content {
			if item == nil || item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
				return fmt.Errorf("list values must be strings")
			}
			out = append(out, item.Value)
		}
		*s = out
		return nil
	case yaml.ScalarNode:
		if node.Tag == "!!null" {
			*s = nil
			return nil
		}
		if node.Tag != "!!str" {
			return fmt.Errorf("expected a string or string list")
		}
		raw := strings.TrimSpace(node.Value)
		if raw == "" {
			*s = []string{node.Value}
			return nil
		}
		*s = strings.Split(raw, ",")
		return nil
	default:
		return fmt.Errorf("expected a string or string list")
	}
}

type rawConfig struct {
	HostEnabled          *bool      `yaml:"enabled"`
	HostPriority         int        `yaml:"priority"`
	HostStore            any        `yaml:"store"`
	ScheduleEnabled      bool       `yaml:"schedule_enabled"`
	Timezone             string     `yaml:"timezone"`
	Times                StringList `yaml:"times"`
	Model                string     `yaml:"model"`
	Message              string     `yaml:"message"`
	EntryProtocol        string     `yaml:"entry_protocol"`
	ExitProtocol         string     `yaml:"exit_protocol"`
	MaxTokens            *int       `yaml:"max_tokens"`
	Providers            StringList `yaml:"providers"`
	IncludeAuthIndices   StringList `yaml:"include_auth_indices"`
	ExcludeAuthIndices   StringList `yaml:"exclude_auth_indices"`
	PhysicalFilesOnly    *bool      `yaml:"physical_files_only"`
	SkipUnavailable      *bool      `yaml:"skip_unavailable"`
	DelayBetweenAuthsRaw string     `yaml:"delay_between_auths"`
	TemporaryPriority    *bool      `yaml:"temporary_priority_override"`
	PrioritySyncRaw      string     `yaml:"priority_sync_timeout"`
	RequestTimeoutRaw    string     `yaml:"request_timeout"`
}

type DailyTime struct {
	Hour   int
	Minute int
	Second int
	Text   string
}

func (t DailyTime) secondsSinceMidnight() int {
	return t.Hour*60*60 + t.Minute*60 + t.Second
}

type Config struct {
	HostEnabled         bool
	ScheduleEnabled     bool
	Timezone            string
	Location            *time.Location
	Times               []DailyTime
	Model               string
	Message             string
	EntryProtocol       string
	ExitProtocol        string
	MaxTokens           int
	Providers           []string
	IncludeAuthIndices  []string
	ExcludeAuthIndices  []string
	PhysicalFilesOnly   bool
	SkipUnavailable     bool
	DelayBetweenAuths   time.Duration
	TemporaryPriority   bool
	PrioritySyncTimeout time.Duration
	RequestTimeout      time.Duration
}

type ConfigSummary struct {
	HostEnabled         bool     `json:"host_enabled"`
	ScheduleEnabled     bool     `json:"schedule_enabled"`
	Timezone            string   `json:"timezone"`
	Times               []string `json:"times"`
	Model               string   `json:"model"`
	EntryProtocol       string   `json:"entry_protocol"`
	ExitProtocol        string   `json:"exit_protocol"`
	MaxTokens           int      `json:"max_tokens"`
	Providers           []string `json:"providers,omitempty"`
	IncludeAuthIndices  []string `json:"include_auth_indices,omitempty"`
	ExcludeAuthIndices  []string `json:"exclude_auth_indices,omitempty"`
	PhysicalFilesOnly   bool     `json:"physical_files_only"`
	SkipUnavailable     bool     `json:"skip_unavailable"`
	DelayBetweenAuths   string   `json:"delay_between_auths"`
	TemporaryPriority   bool     `json:"temporary_priority_override"`
	PrioritySyncTimeout string   `json:"priority_sync_timeout"`
	RequestTimeout      string   `json:"request_timeout"`
	MessageBytes        int      `json:"message_bytes"`
	MessageSHA256       string   `json:"message_sha256,omitempty"`
}

func ParseConfig(raw []byte) (Config, error) {
	decoded := rawConfig{}
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&decoded); err != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return Config{}, fmt.Errorf("decode plugin config: multiple YAML documents are not supported")
			}
			return Config{}, fmt.Errorf("decode plugin config: %w", err)
		}
	}

	hostEnabled := true
	if decoded.HostEnabled != nil {
		hostEnabled = *decoded.HostEnabled
	}
	if !hostEnabled {
		return Config{HostEnabled: false}, nil
	}

	timezone := strings.TrimSpace(decoded.Timezone)
	if timezone == "" {
		timezone = "Local"
	}
	location := time.Local
	if !strings.EqualFold(timezone, "local") {
		var err error
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return Config{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
		}
	}

	maxTokens := defaultMaxTokens
	if decoded.MaxTokens != nil {
		maxTokens = *decoded.MaxTokens
	}
	if maxTokens < 1 || maxTokens > 4096 {
		return Config{}, fmt.Errorf("max_tokens must be between 1 and 4096")
	}

	physicalOnly := true
	if decoded.PhysicalFilesOnly != nil {
		physicalOnly = *decoded.PhysicalFilesOnly
	}
	skipUnavailable := true
	if decoded.SkipUnavailable != nil {
		skipUnavailable = *decoded.SkipUnavailable
	}
	temporaryPriority := false
	if decoded.TemporaryPriority != nil {
		temporaryPriority = *decoded.TemporaryPriority
	}

	delay := defaultDelayBetweenAuths
	if strings.TrimSpace(decoded.DelayBetweenAuthsRaw) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(decoded.DelayBetweenAuthsRaw))
		if err != nil {
			return Config{}, fmt.Errorf("invalid delay_between_auths: %w", err)
		}
		delay = parsed
	}
	if delay < 0 || delay > time.Hour {
		return Config{}, fmt.Errorf("delay_between_auths must be between 0 and 1h")
	}
	prioritySyncTimeout := defaultPrioritySyncTimeout
	if strings.TrimSpace(decoded.PrioritySyncRaw) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(decoded.PrioritySyncRaw))
		if err != nil {
			return Config{}, fmt.Errorf("invalid priority_sync_timeout: %w", err)
		}
		prioritySyncTimeout = parsed
	}
	if prioritySyncTimeout < 100*time.Millisecond || prioritySyncTimeout > time.Minute {
		return Config{}, fmt.Errorf("priority_sync_timeout must be between 100ms and 1m")
	}
	requestTimeout := defaultRequestTimeout
	if strings.TrimSpace(decoded.RequestTimeoutRaw) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(decoded.RequestTimeoutRaw))
		if err != nil {
			return Config{}, fmt.Errorf("invalid request_timeout: %w", err)
		}
		requestTimeout = parsed
	}
	if requestTimeout < time.Second || requestTimeout > 30*time.Minute {
		return Config{}, fmt.Errorf("request_timeout must be between 1s and 30m")
	}

	times, err := normalizeTimes(decoded.Times)
	if err != nil {
		return Config{}, err
	}
	model := strings.TrimSpace(decoded.Model)
	message := decoded.Message
	if len(message) > maxMessageBytes {
		return Config{}, fmt.Errorf("message exceeds %d bytes", maxMessageBytes)
	}
	if decoded.ScheduleEnabled {
		if len(times) == 0 {
			return Config{}, fmt.Errorf("times must contain at least one daily time when schedule_enabled is true")
		}
		if model == "" {
			return Config{}, fmt.Errorf("model is required when schedule_enabled is true")
		}
		if strings.TrimSpace(message) == "" {
			return Config{}, fmt.Errorf("message is required when schedule_enabled is true")
		}
	}

	entryProtocol := strings.ToLower(strings.TrimSpace(decoded.EntryProtocol))
	if entryProtocol == "" {
		entryProtocol = "openai"
	}
	if entryProtocol != "openai" {
		return Config{}, fmt.Errorf("entry_protocol must be openai because the plugin emits an OpenAI Chat Completions request body")
	}
	exitProtocol := strings.ToLower(strings.TrimSpace(decoded.ExitProtocol))
	if exitProtocol == "" {
		exitProtocol = "openai"
	}
	if !supportedExitProtocol(exitProtocol) {
		return Config{}, fmt.Errorf("exit_protocol must be one of openai, openai-response, claude, gemini, codex, antigravity, or interactions")
	}
	providers, err := normalizeFilterStrings("providers", decoded.Providers, true)
	if err != nil {
		return Config{}, err
	}
	includeAuthIndices, err := normalizeFilterStrings("include_auth_indices", decoded.IncludeAuthIndices, false)
	if err != nil {
		return Config{}, err
	}
	excludeAuthIndices, err := normalizeFilterStrings("exclude_auth_indices", decoded.ExcludeAuthIndices, false)
	if err != nil {
		return Config{}, err
	}

	return Config{
		HostEnabled:         true,
		ScheduleEnabled:     decoded.ScheduleEnabled,
		Timezone:            timezone,
		Location:            location,
		Times:               times,
		Model:               model,
		Message:             message,
		EntryProtocol:       entryProtocol,
		ExitProtocol:        exitProtocol,
		MaxTokens:           maxTokens,
		Providers:           providers,
		IncludeAuthIndices:  includeAuthIndices,
		ExcludeAuthIndices:  excludeAuthIndices,
		PhysicalFilesOnly:   physicalOnly,
		SkipUnavailable:     skipUnavailable,
		DelayBetweenAuths:   delay,
		TemporaryPriority:   temporaryPriority,
		PrioritySyncTimeout: prioritySyncTimeout,
		RequestTimeout:      requestTimeout,
	}, nil
}

func supportedExitProtocol(protocol string) bool {
	switch protocol {
	case "openai", "openai-response", "claude", "gemini", "codex", "antigravity", "interactions":
		return true
	default:
		return false
	}
}

func normalizeTimes(values []string) ([]DailyTime, error) {
	if len(values) > maxTimePoints {
		return nil, fmt.Errorf("times contains more than %d entries", maxTimePoints)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]DailyTime, 0, len(values))
	for _, value := range values {
		parsed, err := parseDailyTime(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[parsed.Text]; exists {
			continue
		}
		seen[parsed.Text] = struct{}{}
		out = append(out, parsed)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].secondsSinceMidnight() < out[j].secondsSinceMidnight()
	})
	return out, nil
}

func parseDailyTime(value string) (DailyTime, error) {
	raw := strings.TrimSpace(value)
	parts := strings.Split(raw, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return DailyTime{}, fmt.Errorf("invalid daily time %q; use HH:MM or HH:MM:SS", value)
	}
	parse := func(name, text string, min, max int) (int, error) {
		if len(text) < 1 || len(text) > 2 {
			return 0, fmt.Errorf("invalid %s in daily time %q", name, value)
		}
		v, err := strconv.Atoi(text)
		if err != nil || v < min || v > max {
			return 0, fmt.Errorf("invalid %s in daily time %q", name, value)
		}
		return v, nil
	}
	hour, err := parse("hour", parts[0], 0, 23)
	if err != nil {
		return DailyTime{}, err
	}
	minute, err := parse("minute", parts[1], 0, 59)
	if err != nil {
		return DailyTime{}, err
	}
	second := 0
	if len(parts) == 3 {
		second, err = parse("second", parts[2], 0, 59)
		if err != nil {
			return DailyTime{}, err
		}
	}
	return DailyTime{
		Hour:   hour,
		Minute: minute,
		Second: second,
		Text:   fmt.Sprintf("%02d:%02d:%02d", hour, minute, second),
	}, nil
}

func normalizeStrings(values []string, lower bool) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeFilterStrings(name string, values []string, lower bool) ([]string, error) {
	normalized := normalizeStrings(values, lower)
	if len(values) > 0 && len(normalized) == 0 {
		return nil, fmt.Errorf("%s must contain at least one non-empty value when specified", name)
	}
	return normalized, nil
}

func (c Config) Clone() Config {
	copyConfig := c
	copyConfig.Times = append([]DailyTime(nil), c.Times...)
	copyConfig.Providers = append([]string(nil), c.Providers...)
	copyConfig.IncludeAuthIndices = append([]string(nil), c.IncludeAuthIndices...)
	copyConfig.ExcludeAuthIndices = append([]string(nil), c.ExcludeAuthIndices...)
	return copyConfig
}

func (c Config) Equivalent(other Config) bool {
	if c.HostEnabled != other.HostEnabled || c.ScheduleEnabled != other.ScheduleEnabled ||
		c.Timezone != other.Timezone || c.Model != other.Model || c.Message != other.Message ||
		c.EntryProtocol != other.EntryProtocol || c.ExitProtocol != other.ExitProtocol ||
		c.MaxTokens != other.MaxTokens || c.PhysicalFilesOnly != other.PhysicalFilesOnly ||
		c.SkipUnavailable != other.SkipUnavailable || c.DelayBetweenAuths != other.DelayBetweenAuths ||
		c.TemporaryPriority != other.TemporaryPriority || c.PrioritySyncTimeout != other.PrioritySyncTimeout ||
		c.RequestTimeout != other.RequestTimeout {
		return false
	}
	if len(c.Times) != len(other.Times) || len(c.Providers) != len(other.Providers) ||
		len(c.IncludeAuthIndices) != len(other.IncludeAuthIndices) || len(c.ExcludeAuthIndices) != len(other.ExcludeAuthIndices) {
		return false
	}
	for index := range c.Times {
		if c.Times[index] != other.Times[index] {
			return false
		}
	}
	return equalStrings(c.Providers, other.Providers) &&
		equalStrings(c.IncludeAuthIndices, other.IncludeAuthIndices) &&
		equalStrings(c.ExcludeAuthIndices, other.ExcludeAuthIndices)
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (c Config) Summary() ConfigSummary {
	times := make([]string, 0, len(c.Times))
	for _, item := range c.Times {
		times = append(times, item.Text)
	}
	digest := ""
	if c.Message != "" {
		sum := sha256.Sum256([]byte(c.Message))
		digest = hex.EncodeToString(sum[:])
	}
	return ConfigSummary{
		HostEnabled:         c.HostEnabled,
		ScheduleEnabled:     c.ScheduleEnabled,
		Timezone:            c.Timezone,
		Times:               times,
		Model:               c.Model,
		EntryProtocol:       c.EntryProtocol,
		ExitProtocol:        c.ExitProtocol,
		MaxTokens:           c.MaxTokens,
		Providers:           append([]string(nil), c.Providers...),
		IncludeAuthIndices:  append([]string(nil), c.IncludeAuthIndices...),
		ExcludeAuthIndices:  append([]string(nil), c.ExcludeAuthIndices...),
		PhysicalFilesOnly:   c.PhysicalFilesOnly,
		SkipUnavailable:     c.SkipUnavailable,
		DelayBetweenAuths:   c.DelayBetweenAuths.String(),
		TemporaryPriority:   c.TemporaryPriority,
		PrioritySyncTimeout: c.PrioritySyncTimeout.String(),
		RequestTimeout:      c.RequestTimeout.String(),
		MessageBytes:        len(c.Message),
		MessageSHA256:       digest,
	}
}
