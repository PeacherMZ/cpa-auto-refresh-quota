package refreshquota

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig(nil) error = %v", err)
	}

	if !cfg.HostEnabled {
		t.Fatal("HostEnabled = false, want true")
	}
	if cfg.ScheduleEnabled {
		t.Fatal("ScheduleEnabled = true, want false")
	}
	if cfg.Timezone != "Local" {
		t.Fatalf("Timezone = %q, want Local", cfg.Timezone)
	}
	if cfg.Location != time.Local {
		t.Fatalf("Location = %v, want time.Local", cfg.Location)
	}
	if len(cfg.Times) != 0 {
		t.Fatalf("Times = %#v, want empty", cfg.Times)
	}
	if cfg.EntryProtocol != "openai" || cfg.ExitProtocol != "openai" {
		t.Fatalf("protocols = %q/%q, want openai/openai", cfg.EntryProtocol, cfg.ExitProtocol)
	}
	if cfg.MaxTokens != defaultMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", cfg.MaxTokens, defaultMaxTokens)
	}
	if !cfg.PhysicalFilesOnly {
		t.Fatal("PhysicalFilesOnly = false, want true")
	}
	if !cfg.SkipUnavailable {
		t.Fatal("SkipUnavailable = false, want true")
	}
	if cfg.DelayBetweenAuths != defaultDelayBetweenAuths {
		t.Fatalf("DelayBetweenAuths = %v, want %v", cfg.DelayBetweenAuths, defaultDelayBetweenAuths)
	}
	if cfg.TemporaryPriority {
		t.Fatal("TemporaryPriority = true, want explicit opt-in")
	}
	if cfg.PrioritySyncTimeout != defaultPrioritySyncTimeout {
		t.Fatalf("PrioritySyncTimeout = %v, want %v", cfg.PrioritySyncTimeout, defaultPrioritySyncTimeout)
	}
	if cfg.RequestTimeout != defaultRequestTimeout {
		t.Fatalf("RequestTimeout = %v, want %v", cfg.RequestTimeout, defaultRequestTimeout)
	}
	if len(cfg.Providers) != 0 || len(cfg.IncludeAuthIndices) != 0 || len(cfg.ExcludeAuthIndices) != 0 {
		t.Fatalf("filters = %#v/%#v/%#v, want empty", cfg.Providers, cfg.IncludeAuthIndices, cfg.ExcludeAuthIndices)
	}
}

func TestParseConfigHostDisabledShortCircuitsSemanticValidation(t *testing.T) {
	raw := []byte(`
enabled: false
schedule_enabled: true
timezone: Invalid/Timezone
times: [not-a-time]
model: ""
message: ""
max_tokens: 0
delay_between_auths: definitely-not-a-duration
`)

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig(disabled) error = %v", err)
	}
	if cfg.HostEnabled {
		t.Fatal("HostEnabled = true, want false")
	}
	if !reflect.DeepEqual(cfg, Config{HostEnabled: false}) {
		t.Fatalf("disabled config = %#v, want only HostEnabled=false", cfg)
	}
}

func TestParseConfigAcceptsCPAHostOwnedFields(t *testing.T) {
	raw := []byte(`
enabled: true
priority: 17
store:
  version: 1.0.3
schedule_enabled: false
`)

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if !cfg.HostEnabled {
		t.Fatal("HostEnabled = false, want true")
	}
}

func TestParseConfigAcceptsSequenceAndCommaSeparatedLists(t *testing.T) {
	raw := []byte(`
providers: " Codex, claude, CODEX, , gemini "
include_auth_indices:
  - " auth-A "
  - auth-B
  - auth-A
exclude_auth_indices: " skip-A, skip-B, skip-A, "
physical_files_only: false
skip_unavailable: false
max_tokens: 32
delay_between_auths: 250ms
entry_protocol: " OpenAI "
exit_protocol: " Claude "
`)

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	assertStringsEqual(t, "Providers", cfg.Providers, []string{"codex", "claude", "gemini"})
	assertStringsEqual(t, "IncludeAuthIndices", cfg.IncludeAuthIndices, []string{"auth-A", "auth-B"})
	assertStringsEqual(t, "ExcludeAuthIndices", cfg.ExcludeAuthIndices, []string{"skip-A", "skip-B"})
	if cfg.PhysicalFilesOnly {
		t.Fatal("PhysicalFilesOnly = true, want false")
	}
	if cfg.SkipUnavailable {
		t.Fatal("SkipUnavailable = true, want false")
	}
	if cfg.MaxTokens != 32 {
		t.Fatalf("MaxTokens = %d, want 32", cfg.MaxTokens)
	}
	if cfg.DelayBetweenAuths != 250*time.Millisecond {
		t.Fatalf("DelayBetweenAuths = %v, want 250ms", cfg.DelayBetweenAuths)
	}
	if cfg.EntryProtocol != "openai" || cfg.ExitProtocol != "claude" {
		t.Fatalf("protocols = %q/%q, want openai/claude", cfg.EntryProtocol, cfg.ExitProtocol)
	}
}

func TestStringListRejectsUnsupportedYAMLShapes(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "mapping instead of list",
			raw:     "providers:\n  name: codex\n",
			wantErr: "expected a string or string list",
		},
		{
			name:    "mapping item inside sequence",
			raw:     "providers:\n  - name: codex\n",
			wantErr: "list values must be strings",
		},
		{
			name:    "numeric scalar",
			raw:     "providers: 123\n",
			wantErr: "expected a string or string list",
		},
		{
			name:    "boolean list item",
			raw:     "providers: [codex, true]\n",
			wantErr: "list values must be strings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tt.raw))
			if err == nil {
				t.Fatal("ParseConfig() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseConfig() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseConfigNormalizesSortsAndDeduplicatesDailyTimes(t *testing.T) {
	raw := []byte(`
schedule_enabled: true
timezone: UTC
times: "23:59, 7:5, 07:05:00, 00:00:01, 12:00:30, 23:59:00"
model: " test-model "
message: " ping "
`)

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	want := []DailyTime{
		{Hour: 0, Minute: 0, Second: 1, Text: "00:00:01"},
		{Hour: 7, Minute: 5, Second: 0, Text: "07:05:00"},
		{Hour: 12, Minute: 0, Second: 30, Text: "12:00:30"},
		{Hour: 23, Minute: 59, Second: 0, Text: "23:59:00"},
	}
	if !reflect.DeepEqual(cfg.Times, want) {
		t.Fatalf("Times = %#v, want %#v", cfg.Times, want)
	}
	if cfg.Model != "test-model" || cfg.Message != " ping " {
		t.Fatalf("model/message = %q/%q, want trimmed model and exact message", cfg.Model, cfg.Message)
	}
}

func TestParseConfigLoadsIANATimezone(t *testing.T) {
	cfg, err := ParseConfig([]byte("timezone: Asia/Shanghai\n"))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}
	if cfg.Timezone != "Asia/Shanghai" {
		t.Fatalf("Timezone = %q, want Asia/Shanghai", cfg.Timezone)
	}
	if cfg.Location == nil || cfg.Location.String() != "Asia/Shanghai" {
		t.Fatalf("Location = %v, want Asia/Shanghai", cfg.Location)
	}
	_, offset := time.Date(2026, time.July, 29, 12, 0, 0, 0, cfg.Location).Zone()
	if offset != 8*60*60 {
		t.Fatalf("Asia/Shanghai offset = %d, want %d", offset, 8*60*60)
	}
}

func TestParseConfigValidation(t *testing.T) {
	overlongMessage := strings.Repeat("x", maxMessageBytes+1)
	tooManyTimes := "times:\n" + strings.Repeat("  - 00:00\n", maxTimePoints+1)

	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "invalid yaml", raw: "providers: [", wantErr: "decode plugin config"},
		{name: "multiple yaml documents", raw: "model: first\n---\nmodel: second\n", wantErr: "multiple YAML documents are not supported"},
		{name: "unknown field", raw: "provider: codex\n", wantErr: "field provider not found"},
		{name: "blank providers filter", raw: "providers: \" \"\n", wantErr: "providers must contain at least one non-empty value"},
		{name: "blank include filter", raw: "include_auth_indices: [\"  \"]\n", wantErr: "include_auth_indices must contain at least one non-empty value"},
		{name: "blank exclude filter", raw: "exclude_auth_indices: \" , \"\n", wantErr: "exclude_auth_indices must contain at least one non-empty value"},
		{name: "invalid timezone", raw: "timezone: Invalid/Timezone\n", wantErr: "invalid timezone"},
		{name: "max tokens too small", raw: "max_tokens: 0\n", wantErr: "max_tokens must be between 1 and 4096"},
		{name: "max tokens too large", raw: "max_tokens: 4097\n", wantErr: "max_tokens must be between 1 and 4096"},
		{name: "invalid delay", raw: "delay_between_auths: soon\n", wantErr: "invalid delay_between_auths"},
		{name: "invalid priority sync timeout", raw: "priority_sync_timeout: soon\n", wantErr: "invalid priority_sync_timeout"},
		{name: "invalid request timeout", raw: "request_timeout: soon\n", wantErr: "invalid request_timeout"},
		{name: "unsupported entry protocol", raw: "entry_protocol: responses\n", wantErr: "entry_protocol must be openai"},
		{name: "unsupported exit protocol", raw: "exit_protocol: anthropic\n", wantErr: "exit_protocol must be one of"},
		{name: "negative delay", raw: "delay_between_auths: -1ns\n", wantErr: "delay_between_auths must be between 0 and 1h"},
		{name: "delay over one hour", raw: "delay_between_auths: 1h0m1s\n", wantErr: "delay_between_auths must be between 0 and 1h"},
		{name: "priority sync timeout too short", raw: "priority_sync_timeout: 99ms\n", wantErr: "priority_sync_timeout must be between 100ms and 1m"},
		{name: "priority sync timeout too long", raw: "priority_sync_timeout: 1m1ms\n", wantErr: "priority_sync_timeout must be between 100ms and 1m"},
		{name: "request timeout too short", raw: "request_timeout: 999ms\n", wantErr: "request_timeout must be between 1s and 30m"},
		{name: "request timeout too long", raw: "request_timeout: 30m1s\n", wantErr: "request_timeout must be between 1s and 30m"},
		{name: "missing scheduled times", raw: "schedule_enabled: true\nmodel: m\nmessage: p\n", wantErr: "times must contain at least one daily time"},
		{name: "missing scheduled model", raw: "schedule_enabled: true\ntimes: [08:00]\nmessage: p\n", wantErr: "model is required"},
		{name: "missing scheduled message", raw: "schedule_enabled: true\ntimes: [08:00]\nmodel: m\n", wantErr: "message is required"},
		{name: "message too large", raw: fmt.Sprintf("message: %q\n", overlongMessage), wantErr: fmt.Sprintf("message exceeds %d bytes", maxMessageBytes)},
		{name: "too many times", raw: tooManyTimes, wantErr: fmt.Sprintf("times contains more than %d entries", maxTimePoints)},
		{name: "invalid time shape", raw: "times: [08:00:00:00]\n", wantErr: "use HH:MM or HH:MM:SS"},
		{name: "invalid hour", raw: "times: [24:00]\n", wantErr: "invalid hour"},
		{name: "invalid minute", raw: "times: [23:60]\n", wantErr: "invalid minute"},
		{name: "invalid second", raw: "times: [23:59:60]\n", wantErr: "invalid second"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tt.raw))
			if err == nil {
				t.Fatal("ParseConfig() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseConfig() error = %q, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseConfigValidationBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens int
		delay     string
		wantDelay time.Duration
	}{
		{name: "minimum", maxTokens: 1, delay: "0s", wantDelay: 0},
		{name: "maximum", maxTokens: 4096, delay: "1h", wantDelay: time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := fmt.Sprintf("max_tokens: %d\ndelay_between_auths: %s\n", tt.maxTokens, tt.delay)
			cfg, err := ParseConfig([]byte(raw))
			if err != nil {
				t.Fatalf("ParseConfig() error = %v", err)
			}
			if cfg.MaxTokens != tt.maxTokens {
				t.Fatalf("MaxTokens = %d, want %d", cfg.MaxTokens, tt.maxTokens)
			}
			if cfg.DelayBetweenAuths != tt.wantDelay {
				t.Fatalf("DelayBetweenAuths = %v, want %v", cfg.DelayBetweenAuths, tt.wantDelay)
			}
		})
	}
}

func TestConfigCloneAndSummary(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
schedule_enabled: true
timezone: UTC
times: [08:00, 12:30:15]
model: test-model
message: "hello, quota"
providers: [codex]
include_auth_indices: [auth-1]
exclude_auth_indices: [auth-2]
delay_between_auths: 2s
temporary_priority_override: true
priority_sync_timeout: 3s
request_timeout: 4m
`))
	if err != nil {
		t.Fatalf("ParseConfig() error = %v", err)
	}

	clone := cfg.Clone()
	clone.Times[0].Text = "changed"
	clone.Providers[0] = "changed"
	clone.IncludeAuthIndices[0] = "changed"
	clone.ExcludeAuthIndices[0] = "changed"
	if cfg.Times[0].Text != "08:00:00" || cfg.Providers[0] != "codex" ||
		cfg.IncludeAuthIndices[0] != "auth-1" || cfg.ExcludeAuthIndices[0] != "auth-2" {
		t.Fatalf("Clone() shares mutable slices with original: original = %#v", cfg)
	}

	summary := cfg.Summary()
	if !reflect.DeepEqual(summary.Times, []string{"08:00:00", "12:30:15"}) {
		t.Fatalf("Summary.Times = %#v", summary.Times)
	}
	if summary.MessageBytes != len(cfg.Message) {
		t.Fatalf("MessageBytes = %d, want %d", summary.MessageBytes, len(cfg.Message))
	}
	sum := sha256.Sum256([]byte(cfg.Message))
	wantDigest := hex.EncodeToString(sum[:])
	if summary.MessageSHA256 != wantDigest {
		t.Fatalf("MessageSHA256 = %q, want %q", summary.MessageSHA256, wantDigest)
	}
	if summary.DelayBetweenAuths != "2s" {
		t.Fatalf("DelayBetweenAuths = %q, want 2s", summary.DelayBetweenAuths)
	}
	if !summary.TemporaryPriority || summary.PrioritySyncTimeout != "3s" {
		t.Fatalf("priority summary = enabled %v timeout %q", summary.TemporaryPriority, summary.PrioritySyncTimeout)
	}
	if summary.RequestTimeout != "4m0s" {
		t.Fatalf("RequestTimeout = %q, want 4m0s", summary.RequestTimeout)
	}
}

func assertStringsEqual(t *testing.T, name string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", name, got, want)
	}
}
