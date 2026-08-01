package refreshquota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu sync.Mutex

	auths       []Auth
	runtime     map[string]Auth
	runtimeErr  map[string]error
	listErr     error
	listFn      func(context.Context) ([]Auth, error)
	executeErr  error
	executeResp ModelResponse
	executeFn   func(context.Context, PinnedModelRequest) (ModelResponse, error)
	executed    []PinnedModelRequest
	files       map[string]AuthFile
	fileErr     map[string]error
	saveErr     error
	saveFn      func(context.Context, AuthFile) error
	saved       []AuthFile
	logs        []map[string]any
}

func (h *fakeHost) ListAuths(ctx context.Context) ([]Auth, error) {
	h.mu.Lock()
	listFn := h.listFn
	auths := append([]Auth(nil), h.auths...)
	err := h.listErr
	h.mu.Unlock()
	if listFn != nil {
		return listFn(ctx)
	}
	return auths, err
}

func (h *fakeHost) GetRuntimeAuth(_ context.Context, authIndex string) (Auth, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.runtimeErr[authIndex]; err != nil {
		return Auth{}, err
	}
	return h.runtime[authIndex], nil
}

func (h *fakeHost) GetAuthFile(_ context.Context, authIndex string) (AuthFile, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.fileErr[authIndex]; err != nil {
		return AuthFile{}, err
	}
	file, ok := h.files[authIndex]
	if !ok {
		return AuthFile{}, errors.New("auth file not found")
	}
	file.JSON = append(json.RawMessage(nil), file.JSON...)
	return file, nil
}

func (h *fakeHost) SaveAuthFile(ctx context.Context, file AuthFile) error {
	h.mu.Lock()
	saveFn := h.saveFn
	err := h.saveErr
	h.mu.Unlock()
	if saveFn != nil {
		return saveFn(ctx, file)
	}
	if err != nil {
		return err
	}
	return h.applyAuthFile(file)
}

func (h *fakeHost) applyAuthFile(file AuthFile) error {
	fields, errDecode := decodeAuthFileObject(file.JSON)
	if errDecode != nil {
		return errDecode
	}
	priority := priorityFromRawJSON(fields["priority"])
	h.mu.Lock()
	defer h.mu.Unlock()
	file.JSON = append(json.RawMessage(nil), file.JSON...)
	h.files[file.AuthIndex] = file
	h.saved = append(h.saved, file)
	auth := h.runtime[file.AuthIndex]
	auth.Priority = priority
	h.runtime[file.AuthIndex] = auth
	for index := range h.auths {
		if h.auths[index].AuthIndex == file.AuthIndex {
			h.auths[index].Priority = priority
		}
	}
	return nil
}

func (h *fakeHost) ExecutePinned(ctx context.Context, req PinnedModelRequest) (ModelResponse, error) {
	h.mu.Lock()
	h.executed = append(h.executed, clonePinnedRequest(req))
	executeFn := h.executeFn
	response := h.executeResp
	err := h.executeErr
	h.mu.Unlock()
	if executeFn != nil {
		return executeFn(ctx, req)
	}
	return response, err
}

func (h *fakeHost) Log(_ context.Context, _ string, _ string, fields map[string]any) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cloned := make(map[string]any, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	h.logs = append(h.logs, cloned)
	return nil
}

func clonePinnedRequest(req PinnedModelRequest) PinnedModelRequest {
	cloned := req
	cloned.Body = append([]byte(nil), req.Body...)
	cloned.Headers = req.Headers.Clone()
	return cloned
}

func TestServiceDryRunDoesNotExecuteModel(t *testing.T) {
	host := newReadyFakeHost()
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{DryRun: true})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if !report.DryRun || report.TargetCount != 2 || len(report.Results) != 2 {
		t.Fatalf("dry-run report = %#v", report)
	}
	for _, result := range report.Results {
		if result.Outcome != "selected" {
			t.Fatalf("dry-run result = %#v, want selected", result)
		}
	}
	host.mu.Lock()
	executed := len(host.executed)
	host.mu.Unlock()
	if executed != 0 {
		t.Fatalf("ExecutePinned calls = %d, want 0", executed)
	}

	statusJSON, errMarshal := json.Marshal(service.Status())
	if errMarshal != nil {
		t.Fatalf("marshal status: %v", errMarshal)
	}
	if strings.Contains(string(statusJSON), "exact prompt") {
		t.Fatalf("status leaks configured message: %s", statusJSON)
	}
}

func TestServiceRechecksRuntimeAndPinsExactAuthID(t *testing.T) {
	host := newReadyFakeHost()
	host.runtime["auth-a"] = Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "memory", Disabled: true}
	host.executeResp = ModelResponse{StatusCode: http.StatusOK, Body: []byte(`{"ok":true}`)}
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.SuccessCount != 1 || report.SkippedCount != 1 || report.FailedCount != 0 {
		t.Fatalf("run counts = success %d failed %d skipped %d", report.SuccessCount, report.FailedCount, report.SkippedCount)
	}

	host.mu.Lock()
	requests := append([]PinnedModelRequest(nil), host.executed...)
	host.mu.Unlock()
	if len(requests) != 1 || requests[0].AuthID != "id-b" {
		t.Fatalf("pinned requests = %#v, want only id-b", requests)
	}
	if requests[0].Model != "test-model" || requests[0].EntryProtocol != "openai" || requests[0].ExitProtocol != "openai" {
		t.Fatalf("pinned request routing = %#v", requests[0])
	}
	var body map[string]any
	if errDecode := json.Unmarshal(requests[0].Body, &body); errDecode != nil {
		t.Fatalf("decode model body: %v", errDecode)
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("model body messages = %#v", body["messages"])
	}
	message := messages[0].(map[string]any)
	if message["content"] != " exact prompt " {
		t.Fatalf("message content = %#v, want exact whitespace", message["content"])
	}
}

func TestServiceFailsClosedWhenRuntimeAuthIDChanges(t *testing.T) {
	host := newReadyFakeHost()
	host.auths = host.auths[:1]
	host.runtime["auth-a"] = Auth{ID: "different-id", AuthIndex: "auth-a", Provider: "codex", Source: "memory"}
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.FailedCount != 1 || len(report.Results) != 1 || report.Results[0].Error != "runtime auth id mismatch" {
		t.Fatalf("run report = %#v", report)
	}
	host.mu.Lock()
	executed := len(host.executed)
	host.mu.Unlock()
	if executed != 0 {
		t.Fatalf("ExecutePinned calls = %d, want 0", executed)
	}
}

func TestServiceFailsClosedOnDuplicateAuthIdentities(t *testing.T) {
	host := newReadyFakeHost()
	host.auths = append(host.auths, Auth{ID: "id-c", AuthIndex: "auth-a", Provider: "codex", Source: "memory"})
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.Error != "host auth listing contains duplicate auth identities" {
		t.Fatalf("run report = %#v", report)
	}
	host.mu.Lock()
	executed := len(host.executed)
	host.mu.Unlock()
	if executed != 0 {
		t.Fatalf("ExecutePinned calls = %d, want 0", executed)
	}
}

func TestServiceSingleFlight(t *testing.T) {
	host := newReadyFakeHost()
	host.auths = host.auths[:1]
	started := make(chan struct{})
	release := make(chan struct{})
	host.executeFn = func(context.Context, PinnedModelRequest) (ModelResponse, error) {
		close(started)
		<-release
		return ModelResponse{StatusCode: http.StatusOK}, nil
	}
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("first StartManual() error = %v", errStart)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first model call did not start")
	}
	if _, errSecond := service.StartManual(ManualRunOptions{}); !errors.Is(errSecond, ErrBusy) {
		t.Fatalf("second StartManual() error = %v, want ErrBusy", errSecond)
	}
	close(release)
	report := waitForRun(t, service, runID)
	if report.SuccessCount != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestInvalidReconfigureCancelsRemainingCredentialsAndStopsOldConfig(t *testing.T) {
	host := newReadyFakeHost()
	firstFinished := make(chan struct{})
	host.executeFn = func(context.Context, PinnedModelRequest) (ModelResponse, error) {
		select {
		case <-firstFinished:
		default:
			close(firstFinished)
		}
		return ModelResponse{StatusCode: http.StatusOK}, nil
	}
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "delay_between_auths: 30s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	select {
	case <-firstFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("first credential did not execute")
	}
	if errApply := service.ApplyConfig([]byte("timezone: Invalid/Timezone\n")); errApply == nil {
		t.Fatal("ApplyConfig(invalid) error = nil")
	}
	report := waitForRun(t, service, runID)
	if !report.Canceled {
		t.Fatalf("report.Canceled = false, report = %#v", report)
	}
	host.mu.Lock()
	executed := len(host.executed)
	host.mu.Unlock()
	if executed != 1 {
		t.Fatalf("ExecutePinned calls = %d, want 1", executed)
	}
	status := service.Status()
	if status.ConfigError == "" || status.Config.ScheduleEnabled {
		t.Fatalf("status after invalid config = %#v", status)
	}
}

func TestEquivalentReconfigureDoesNotCancelActiveRun(t *testing.T) {
	host := newReadyFakeHost()
	host.auths = host.auths[:1]
	started := make(chan struct{})
	release := make(chan struct{})
	host.executeFn = func(ctx context.Context, _ PinnedModelRequest) (ModelResponse, error) {
		close(started)
		select {
		case <-ctx.Done():
			return ModelResponse{}, ctx.Err()
		case <-release:
			return ModelResponse{StatusCode: http.StatusOK}, nil
		}
	}
	service := NewService(host)
	t.Cleanup(service.Shutdown)
	rawConfig := []byte("model: test-model\nmessage: exact\ndelay_between_auths: 0s\n")
	if errApply := service.ApplyConfig(rawConfig); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	generation := service.Status().ConfigGeneration
	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	if errApply := service.ApplyConfig(rawConfig); errApply != nil {
		t.Fatalf("equivalent ApplyConfig() error = %v", errApply)
	}
	if got := service.Status().ConfigGeneration; got != generation {
		t.Fatalf("ConfigGeneration = %d after equivalent reconfigure, want %d", got, generation)
	}
	select {
	case report := <-func() chan *RunReport {
		result := make(chan *RunReport, 1)
		go func() {
			time.Sleep(100 * time.Millisecond)
			result <- service.Status().LastRun
		}()
		return result
	}():
		if report != nil {
			t.Fatalf("run finished before release: %#v", report)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("equivalent reconfigure check timed out")
	}
	close(release)
	report := waitForRun(t, service, runID)
	if report.Canceled || report.SuccessCount != 1 {
		t.Fatalf("run after equivalent reconfigure = %#v", report)
	}
}

func TestShutdownRejectsNewRuns(t *testing.T) {
	host := newReadyFakeHost()
	service := NewService(host)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")
	service.Shutdown()
	if _, errStart := service.StartManual(ManualRunOptions{}); !errors.Is(errStart, ErrStopped) {
		t.Fatalf("StartManual() after shutdown error = %v, want ErrStopped", errStart)
	}
	if !service.Status().Stopped {
		t.Fatal("Status().Stopped = false after Shutdown")
	}
}

func TestShutdownMarksRunCanceledDuringFinalCredential(t *testing.T) {
	host := newReadyFakeHost()
	host.auths = host.auths[:1]
	started := make(chan struct{})
	host.executeFn = func(ctx context.Context, _ PinnedModelRequest) (ModelResponse, error) {
		close(started)
		<-ctx.Done()
		return ModelResponse{}, ctx.Err()
	}
	service := NewService(host)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("model call did not start")
	}
	service.Shutdown()
	report := service.Status().LastRun
	if report == nil || report.RunID != runID || !report.Canceled {
		t.Fatalf("last run after shutdown = %#v, want canceled run %s", report, runID)
	}
}

func TestShutdownMarksRunCanceledDuringAuthListing(t *testing.T) {
	host := newReadyFakeHost()
	started := make(chan struct{})
	var startedOnce sync.Once
	host.listFn = func(ctx context.Context) ([]Auth, error) {
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	service := NewService(host)
	applyTestConfig(t, service, "delay_between_auths: 0s\n")

	runID, errStart := service.StartManual(ManualRunOptions{})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("auth listing did not start")
	}
	service.Shutdown()
	report := service.Status().LastRun
	if report == nil || report.RunID != runID || !report.Canceled {
		t.Fatalf("last run after shutdown = %#v, want canceled run %s", report, runID)
	}
}

func TestSafeExecutionErrorDoesNotEchoArbitraryHostText(t *testing.T) {
	secret := errors.New("upstream response contained token=secret and auth_id details")
	if got := safeExecutionError(secret); got != "host model execution failed" || strings.Contains(got, "secret") {
		t.Fatalf("safeExecutionError(secret) = %q", got)
	}
	if got := safeExecutionError(errors.New("CPA scheduler did not select the requested auth")); got != "CPA scheduler did not select target auth" {
		t.Fatalf("safeExecutionError(scheduler missing) = %q", got)
	}
	if got := safeExecutionError(errors.New("host_call_failed: target_auth_unavailable: requested auth is not available to the CPA scheduler")); got != "target auth is unavailable to CPA scheduler" {
		t.Fatalf("safeExecutionError(target unavailable) = %q", got)
	}
}

func newReadyFakeHost() *fakeHost {
	authA := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "memory"}
	authB := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "memory"}
	return &fakeHost{
		auths:       []Auth{authA, authB},
		runtime:     map[string]Auth{"auth-a": authA, "auth-b": authB},
		runtimeErr:  make(map[string]error),
		files:       make(map[string]AuthFile),
		fileErr:     make(map[string]error),
		executeResp: ModelResponse{StatusCode: http.StatusOK},
	}
}

func applyTestConfig(t *testing.T, service *Service, extra string) {
	t.Helper()
	raw := []byte("model: test-model\nmessage: ' exact prompt '\nphysical_files_only: true\nskip_unavailable: true\n" + extra)
	if errApply := service.ApplyConfig(raw); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
}

func waitForRun(t *testing.T, service *Service, runID string) *RunReport {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if status.LastRun != nil && status.LastRun.RunID == runID {
			return status.LastRun
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish; status = %#v", runID, service.Status())
	return nil
}
