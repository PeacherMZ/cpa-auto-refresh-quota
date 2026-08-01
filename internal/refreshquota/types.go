package refreshquota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

var (
	ErrBusy            = errors.New("a refresh run is already active")
	ErrStopped         = errors.New("service is stopped")
	ErrNotConfigured   = errors.New("model and message must be configured")
	ErrDuplicate       = errors.New("occurrence was already handled")
	ErrAuthFileChanged = errors.New("auth file changed concurrently")
)

// Auth describes the non-secret credential metadata used by this plugin.
type Auth struct {
	ID          string `json:"id,omitempty"`
	AuthIndex   string `json:"auth_index,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Status      string `json:"status,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
	RuntimeOnly bool   `json:"runtime_only,omitempty"`
	Source      string `json:"source,omitempty"`
	Priority    int    `json:"priority,omitempty"`
}

// AuthFile is the physical credential payload exposed by CPA host auth callbacks.
// JSON can contain secrets and must never be logged or copied into run reports.
type AuthFile struct {
	AuthIndex      string          `json:"auth_index"`
	Name           string          `json:"name"`
	Path           string          `json:"path"`
	JSON           json.RawMessage `json:"json"`
	ExpectedSHA256 string          `json:"-"`
}

// PinnedModelRequest asks the plugin host bridge to execute through one exact auth ID.
// The stock CPA bridge implements this with host.model.execute plus scheduler.pick.
type PinnedModelRequest struct {
	AuthID        string
	EntryProtocol string
	ExitProtocol  string
	Model         string
	Body          []byte
	Headers       http.Header
	Query         url.Values
	Alt           string
}

type ModelResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Host is the subset of CPA host callbacks used by the scheduler.
type Host interface {
	ListAuths(context.Context) ([]Auth, error)
	GetRuntimeAuth(context.Context, string) (Auth, error)
	GetAuthFile(context.Context, string) (AuthFile, error)
	SaveAuthFile(context.Context, AuthFile) error
	ExecutePinned(context.Context, PinnedModelRequest) (ModelResponse, error)
	Log(context.Context, string, string, map[string]any) error
}

type ManualRunOptions struct {
	DryRun      bool     `json:"dry_run"`
	AuthIndices []string `json:"auth_indices,omitempty"`
}

type AuthResult struct {
	AuthIndex                string `json:"auth_index"`
	Provider                 string `json:"provider,omitempty"`
	Priority                 int    `json:"priority"`
	PriorityOverrideRequired bool   `json:"priority_override_required,omitempty"`
	PriorityOverrideTo       int    `json:"priority_override_to,omitempty"`
	Outcome                  string `json:"outcome"`
	StatusCode               int    `json:"status_code,omitempty"`
	ResponseBytes            int    `json:"response_bytes,omitempty"`
	DurationMS               int64  `json:"duration_ms,omitempty"`
	Error                    string `json:"error,omitempty"`
}

type RunReport struct {
	RunID        string       `json:"run_id"`
	Trigger      string       `json:"trigger"`
	OccurrenceID string       `json:"occurrence_id,omitempty"`
	DryRun       bool         `json:"dry_run"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   *time.Time   `json:"finished_at,omitempty"`
	DurationMS   int64        `json:"duration_ms,omitempty"`
	TargetCount  int          `json:"target_count"`
	SuccessCount int          `json:"success_count"`
	FailedCount  int          `json:"failed_count"`
	SkippedCount int          `json:"skipped_count"`
	Canceled     bool         `json:"canceled,omitempty"`
	Error        string       `json:"error,omitempty"`
	Results      []AuthResult `json:"results,omitempty"`
}

type NextSchedule struct {
	OccurrenceID string    `json:"occurrence_id"`
	Local        time.Time `json:"local"`
	UTC          time.Time `json:"utc"`
}

type Status struct {
	ConfigGeneration        uint64        `json:"config_generation"`
	Config                  ConfigSummary `json:"config"`
	ConfigError             string        `json:"config_error,omitempty"`
	PriorityRecoveryPending bool          `json:"priority_recovery_pending"`
	PriorityRecoveryError   string        `json:"priority_recovery_error,omitempty"`
	Next                    *NextSchedule `json:"next,omitempty"`
	Running                 bool          `json:"running"`
	ActiveRun               *RunReport    `json:"active_run,omitempty"`
	LastRun                 *RunReport    `json:"last_run,omitempty"`
	SkippedOccurrences      uint64        `json:"skipped_occurrences"`
	LastSkippedOccurrence   string        `json:"last_skipped_occurrence,omitempty"`
	Stopped                 bool          `json:"stopped"`
}

func cloneRunReport(report *RunReport) *RunReport {
	if report == nil {
		return nil
	}
	cloned := *report
	if report.FinishedAt != nil {
		finished := *report.FinishedAt
		cloned.FinishedAt = &finished
	}
	cloned.Results = append([]AuthResult(nil), report.Results...)
	return &cloned
}
