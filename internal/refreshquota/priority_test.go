package refreshquota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestPriorityOverrideRestoresOnlyPriorityAndPreservesNewTokens(t *testing.T) {
	original := json.RawMessage(`{"type":"codex","access_token":"old","priority":"3","custom":{"large":9007199254740993}}`)
	modified, marker, errBuild := buildPriorityOverrideJSON(original, "auth-a", "run-a", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	fields, errDecode := decodeAuthFileObject(modified)
	if errDecode != nil {
		t.Fatalf("decode modified JSON: %v", errDecode)
	}
	if got := priorityFromRawJSON(fields["priority"]); got != 10 {
		t.Fatalf("modified priority = %d, want 10", got)
	}
	fields["access_token"] = json.RawMessage(`"new"`)
	current, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		t.Fatalf("marshal current JSON: %v", errMarshal)
	}

	restored, expectedPriority, changed, external, errRestore := restorePriorityOverrideJSON(current, &marker)
	if errRestore != nil {
		t.Fatalf("restorePriorityOverrideJSON() error = %v", errRestore)
	}
	if !changed || external || expectedPriority != 3 {
		t.Fatalf("restore metadata = changed %v external %v expected %d", changed, external, expectedPriority)
	}
	restoredFields, errDecode := decodeAuthFileObject(restored)
	if errDecode != nil {
		t.Fatalf("decode restored JSON: %v", errDecode)
	}
	if string(restoredFields["access_token"]) != `"new"` || string(restoredFields["priority"]) != `"3"` {
		t.Fatalf("restored fields = %#v", restoredFields)
	}
	if _, exists := restoredFields[priorityOverrideMarkerKey]; exists {
		t.Fatal("priority override marker remains after restore")
	}
	if string(restoredFields["custom"]) != `{"large":9007199254740993}` {
		t.Fatalf("large numeric metadata changed: %s", restoredFields["custom"])
	}
}

func TestPriorityRestorePreservesExternalPriorityChange(t *testing.T) {
	modified, marker, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":1}`), "auth-a", "run-a", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	fields, _ := decodeAuthFileObject(modified)
	fields["priority"] = json.RawMessage(`7`)
	current, _ := json.Marshal(fields)
	restored, expectedPriority, changed, external, errRestore := restorePriorityOverrideJSON(current, &marker)
	if errRestore != nil {
		t.Fatalf("restorePriorityOverrideJSON() error = %v", errRestore)
	}
	if !changed || !external || expectedPriority != 7 {
		t.Fatalf("restore metadata = changed %v external %v expected %d", changed, external, expectedPriority)
	}
	restoredFields, _ := decodeAuthFileObject(restored)
	if got := priorityFromRawJSON(restoredFields["priority"]); got != 7 {
		t.Fatalf("external priority = %d, want 7", got)
	}
	if _, exists := restoredFields[priorityOverrideMarkerKey]; exists {
		t.Fatal("priority override marker remains after external change")
	}
}

func TestServiceTemporarilyAlignsLowerPriorityCredential(t *testing.T) {
	host := newReadyFakeHost()
	authLow := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 1}
	authHigh := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{authLow, authHigh}
	host.runtime = map[string]Auth{"auth-a": authLow, "auth-b": authHigh}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","access_token":"old","priority":1}`)},
		"auth-b": {AuthIndex: "auth-b", Name: "b.json", Path: `C:\auth\b.json`, JSON: json.RawMessage(`{"type":"codex","priority":10}`)},
	}
	host.executeFn = func(_ context.Context, req PinnedModelRequest) (ModelResponse, error) {
		host.mu.Lock()
		if got := host.runtime["auth-a"].Priority; got != 10 {
			host.mu.Unlock()
			return ModelResponse{}, fmt.Errorf("runtime priority during request = %d, want 10", got)
		}
		file := host.files["auth-a"]
		fields, _ := decodeAuthFileObject(file.JSON)
		fields["access_token"] = json.RawMessage(`"refreshed"`)
		file.JSON, _ = json.Marshal(fields)
		host.files["auth-a"] = file
		host.mu.Unlock()
		if req.AuthID != "id-a" {
			return ModelResponse{}, fmt.Errorf("pinned auth ID = %q, want id-a", req.AuthID)
		}
		return ModelResponse{StatusCode: http.StatusOK}, nil
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "temporary_priority_override: true\npriority_sync_timeout: 500ms\ndelay_between_auths: 0s\n")
	runID, errStart := service.StartManual(ManualRunOptions{AuthIndices: []string{"auth-a"}})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.SuccessCount != 1 || report.FailedCount != 0 || len(report.Results) != 1 {
		t.Fatalf("run report = %#v", report)
	}
	result := report.Results[0]
	if !result.PriorityOverrideRequired || result.PriorityOverrideTo != 10 || result.Priority != 1 {
		t.Fatalf("priority result = %#v", result)
	}
	host.mu.Lock()
	file := host.files["auth-a"]
	runtimePriority := host.runtime["auth-a"].Priority
	saveCount := len(host.saved)
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(file.JSON)
	if runtimePriority != 1 || priorityFromRawJSON(fields["priority"]) != 1 || saveCount != 2 {
		t.Fatalf("restored priority runtime=%d file=%d saves=%d", runtimePriority, priorityFromRawJSON(fields["priority"]), saveCount)
	}
	if string(fields["access_token"]) != `"refreshed"` {
		t.Fatalf("refreshed token was overwritten: %s", fields["access_token"])
	}
	if _, exists := fields[priorityOverrideMarkerKey]; exists {
		t.Fatal("priority marker remains after successful run")
	}
}

func TestServiceRecoversStalePriorityOverrideBeforeDryRun(t *testing.T) {
	host := newReadyFakeHost()
	authLow := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 1}
	authHigh := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "file", Priority: 10}
	modified, _, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":1}`), "auth-a", "crashed-run", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	authLow.Priority = 10
	host.auths = []Auth{authLow, authHigh}
	host.runtime = map[string]Auth{"auth-a": authLow, "auth-b": authHigh}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: modified},
		"auth-b": {AuthIndex: "auth-b", Name: "b.json", Path: `C:\auth\b.json`, JSON: json.RawMessage(`{"type":"codex","priority":10}`)},
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "temporary_priority_override: true\npriority_sync_timeout: 500ms\ndelay_between_auths: 0s\n")
	runID, errStart := service.StartManual(ManualRunOptions{DryRun: true, AuthIndices: []string{"auth-a"}})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if len(report.Results) != 1 || report.Results[0].Priority != 1 || !report.Results[0].PriorityOverrideRequired {
		t.Fatalf("dry-run report after recovery = %#v", report)
	}
	host.mu.Lock()
	file := host.files["auth-a"]
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(file.JSON)
	if _, exists := fields[priorityOverrideMarkerKey]; exists {
		t.Fatal("stale marker remains after recovery")
	}
}

func TestHighestPriorityCoversMixedProviderRoute(t *testing.T) {
	highest, found := highestAvailablePriority([]Auth{
		{Provider: "codex", Priority: 4},
		{Provider: "Codex", Priority: 9},
		{Provider: "claude", Priority: 30},
		{Provider: "codex", Priority: 100, Disabled: true},
		{Provider: "gemini", Priority: 200, Unavailable: true},
	})
	if !found || highest != 200 {
		t.Fatalf("highest priority = %d, found = %v", highest, found)
	}
}

func TestPrioritySyncTimeoutValidationSupportsShortTests(t *testing.T) {
	cfg, err := ParseConfig([]byte("priority_sync_timeout: 100ms\n"))
	if err != nil || cfg.PrioritySyncTimeout != 100*time.Millisecond {
		t.Fatalf("priority sync config = %v, error = %v", cfg.PrioritySyncTimeout, err)
	}
}

func TestBeginPriorityOverrideRetriesConcurrentFileChange(t *testing.T) {
	host := newReadyFakeHost()
	auth := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 1}
	host.auths = []Auth{auth}
	host.runtime = map[string]Auth{"auth-a": auth}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","access_token":"old","priority":1}`)},
	}
	saveAttempts := 0
	host.saveFn = func(_ context.Context, file AuthFile) error {
		saveAttempts++
		if saveAttempts == 1 {
			host.mu.Lock()
			current := host.files["auth-a"]
			fields, _ := decodeAuthFileObject(current.JSON)
			fields["access_token"] = json.RawMessage(`"refreshed-concurrently"`)
			current.JSON, _ = json.Marshal(fields)
			host.files["auth-a"] = current
			host.mu.Unlock()
			return ErrAuthFileChanged
		}
		return host.applyAuthFile(file)
	}

	service := NewService(host)
	cfg, errConfig := ParseConfig([]byte("temporary_priority_override: true\npriority_sync_timeout: 500ms\n"))
	if errConfig != nil {
		t.Fatalf("ParseConfig() error = %v", errConfig)
	}
	lease, errBegin := service.beginPriorityOverride(context.Background(), cfg, "run-a", auth, 10)
	if errBegin != nil {
		t.Fatalf("beginPriorityOverride() error = %v", errBegin)
	}
	if lease == nil || saveAttempts != 2 {
		t.Fatalf("priority lease = %#v, save attempts = %d", lease, saveAttempts)
	}
	if errRestore := service.restorePriorityOverride(cfg, "run-a", lease); errRestore != nil {
		t.Fatalf("restorePriorityOverride() error = %v", errRestore)
	}

	host.mu.Lock()
	file := host.files["auth-a"]
	runtimePriority := host.runtime["auth-a"].Priority
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(file.JSON)
	if runtimePriority != 1 || priorityFromRawJSON(fields["priority"]) != 1 {
		t.Fatalf("restored priority runtime=%d file=%d", runtimePriority, priorityFromRawJSON(fields["priority"]))
	}
	if string(fields["access_token"]) != `"refreshed-concurrently"` {
		t.Fatalf("concurrent token was overwritten: %s", fields["access_token"])
	}
}

func TestRestorePriorityOverrideRetriesAndPreservesLatestToken(t *testing.T) {
	host := newReadyFakeHost()
	modified, marker, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","access_token":"old","priority":1}`), "auth-a", "run-a", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	auth := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{auth}
	host.runtime = map[string]Auth{"auth-a": auth}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: modified},
	}
	saveAttempts := 0
	host.saveFn = func(_ context.Context, file AuthFile) error {
		saveAttempts++
		if saveAttempts == 1 {
			host.mu.Lock()
			current := host.files["auth-a"]
			fields, _ := decodeAuthFileObject(current.JSON)
			fields["access_token"] = json.RawMessage(`"newest"`)
			current.JSON, _ = json.Marshal(fields)
			host.files["auth-a"] = current
			host.mu.Unlock()
			return ErrAuthFileChanged
		}
		return host.applyAuthFile(file)
	}

	service := NewService(host)
	cfg, _ := ParseConfig([]byte("priority_sync_timeout: 500ms\n"))
	lease := &priorityOverrideLease{AuthIndex: "auth-a", AuthID: "id-a", Name: "a.json", Marker: marker}
	if errRestore := service.restorePriorityOverride(cfg, "run-a", lease); errRestore != nil {
		t.Fatalf("restorePriorityOverride() error = %v", errRestore)
	}
	if saveAttempts != 2 {
		t.Fatalf("save attempts = %d, want 2", saveAttempts)
	}
	host.mu.Lock()
	file := host.files["auth-a"]
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(file.JSON)
	if priorityFromRawJSON(fields["priority"]) != 1 || string(fields["access_token"]) != `"newest"` {
		t.Fatalf("restored fields = %#v", fields)
	}
	if _, exists := fields[priorityOverrideMarkerKey]; exists {
		t.Fatal("priority marker remains after restoration retry")
	}
}

func TestRestorePriorityOverrideWaitsForExternallyRestoredRuntime(t *testing.T) {
	host := newReadyFakeHost()
	_, marker, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":1}`), "auth-a", "run-a", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	auth := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{auth}
	host.runtime = map[string]Auth{"auth-a": auth}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","priority":7}`)},
	}
	go func() {
		time.Sleep(75 * time.Millisecond)
		host.mu.Lock()
		runtimeAuth := host.runtime["auth-a"]
		runtimeAuth.Priority = 7
		host.runtime["auth-a"] = runtimeAuth
		host.mu.Unlock()
	}()

	service := NewService(host)
	cfg, _ := ParseConfig([]byte("priority_sync_timeout: 500ms\n"))
	lease := &priorityOverrideLease{AuthIndex: "auth-a", AuthID: "id-a", Name: "a.json", Marker: marker}
	started := time.Now()
	if errRestore := service.restorePriorityOverride(cfg, "run-a", lease); errRestore != nil {
		t.Fatalf("restorePriorityOverride() error = %v", errRestore)
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("restore returned before CPA runtime caught up: %v", elapsed)
	}
	host.mu.Lock()
	saveCount := len(host.saved)
	host.mu.Unlock()
	if saveCount != 0 {
		t.Fatalf("SaveAuthFile calls = %d, want 0 for external restoration", saveCount)
	}
}

func TestWatcherEquivalentReconfigureDoesNotInterruptPriorityLease(t *testing.T) {
	host := newReadyFakeHost()
	authLow := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 1}
	authHigh := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{authLow, authHigh}
	host.runtime = map[string]Auth{"auth-a": authLow, "auth-b": authHigh}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","priority":1}`)},
		"auth-b": {AuthIndex: "auth-b", Name: "b.json", Path: `C:\auth\b.json`, JSON: json.RawMessage(`{"type":"codex","priority":10}`)},
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	rawConfig := []byte("model: test-model\nmessage: exact\ntemporary_priority_override: true\npriority_sync_timeout: 500ms\ndelay_between_auths: 0s\n")
	if errApply := service.ApplyConfig(rawConfig); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	generation := service.Status().ConfigGeneration
	host.saveFn = func(_ context.Context, file AuthFile) error {
		if errSave := host.applyAuthFile(file); errSave != nil {
			return errSave
		}
		return service.ApplyConfig(rawConfig)
	}

	runID, errStart := service.StartManual(ManualRunOptions{AuthIndices: []string{"auth-a"}})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.Canceled || report.SuccessCount != 1 || report.FailedCount != 0 {
		t.Fatalf("run after watcher reconfigure = %#v", report)
	}
	if got := service.Status().ConfigGeneration; got != generation {
		t.Fatalf("ConfigGeneration = %d after watcher reconfigure, want %d", got, generation)
	}
}

func TestRequestTimeoutStillRestoresCredentialPriority(t *testing.T) {
	host := newReadyFakeHost()
	authLow := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 1}
	authHigh := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{authLow, authHigh}
	host.runtime = map[string]Auth{"auth-a": authLow, "auth-b": authHigh}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","priority":1}`)},
		"auth-b": {AuthIndex: "auth-b", Name: "b.json", Path: `C:\auth\b.json`, JSON: json.RawMessage(`{"type":"codex","priority":10}`)},
	}
	host.executeFn = func(ctx context.Context, _ PinnedModelRequest) (ModelResponse, error) {
		<-ctx.Done()
		return ModelResponse{}, ctx.Err()
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	applyTestConfig(t, service, "temporary_priority_override: true\npriority_sync_timeout: 500ms\ndelay_between_auths: 0s\n")
	service.mu.Lock()
	service.config.RequestTimeout = 25 * time.Millisecond
	service.mu.Unlock()
	runID, errStart := service.StartManual(ManualRunOptions{AuthIndices: []string{"auth-a"}})
	if errStart != nil {
		t.Fatalf("StartManual() error = %v", errStart)
	}
	report := waitForRun(t, service, runID)
	if report.FailedCount != 1 || len(report.Results) != 1 || report.Results[0].Error != "context deadline exceeded" {
		t.Fatalf("timeout report = %#v", report)
	}
	host.mu.Lock()
	file := host.files["auth-a"]
	runtimePriority := host.runtime["auth-a"].Priority
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(file.JSON)
	if runtimePriority != 1 || priorityFromRawJSON(fields["priority"]) != 1 {
		t.Fatalf("priority after timeout runtime=%d file=%d", runtimePriority, priorityFromRawJSON(fields["priority"]))
	}
	if _, exists := fields[priorityOverrideMarkerKey]; exists {
		t.Fatal("priority marker remains after timed out request")
	}
}

func TestStartupRecoveryWaitsForAuthManagerAndRunsWhenOverrideDisabled(t *testing.T) {
	host := newReadyFakeHost()
	modified, _, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":1}`), "auth-a", "crashed-run", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	auth := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{auth}
	host.runtime = map[string]Auth{"auth-a": auth}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: modified},
	}
	listCalls := 0
	host.listFn = func(context.Context) ([]Auth, error) {
		listCalls++
		if listCalls < 3 {
			return nil, nil
		}
		return []Auth{auth}, nil
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	if errApply := service.ApplyConfig(nil); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		host.mu.Lock()
		file := host.files["auth-a"]
		host.mu.Unlock()
		fields, _ := decodeAuthFileObject(file.JSON)
		_, markerPresent := fields[priorityOverrideMarkerKey]
		if !status.PriorityRecoveryPending && !markerPresent {
			if status.PriorityRecoveryError != "" {
				t.Fatalf("PriorityRecoveryError = %q", status.PriorityRecoveryError)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("startup priority recovery did not complete: %#v", service.Status())
}

func TestStartupRecoveryFailureIsExposedInStatus(t *testing.T) {
	host := newReadyFakeHost()
	auth := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{auth}
	host.runtime = map[string]Auth{"auth-a": auth}
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "a.json", Path: `C:\auth\a.json`, JSON: json.RawMessage(`{"type":"codex","priority":10,"_cpa_auto_refresh_quota_priority_override":{}}`)},
	}

	service := NewService(host)
	t.Cleanup(service.Shutdown)
	if errApply := service.ApplyConfig(nil); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if !status.PriorityRecoveryPending && status.PriorityRecoveryError != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("priority recovery failure was not exposed: %#v", service.Status())
}

func TestStaleRecoveryHandlesMultipleAuthIndicesForOnePhysicalFile(t *testing.T) {
	host := newReadyFakeHost()
	modified, _, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":1}`), "auth-a", "crashed-run", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	authA := Auth{ID: "id-a", AuthIndex: "auth-a", Provider: "codex", Source: "file", Priority: 10}
	authB := Auth{ID: "id-b", AuthIndex: "auth-b", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{authB, authA}
	host.runtime = map[string]Auth{"auth-a": authA, "auth-b": authB}
	sharedPath := `C:\auth\shared.json`
	host.files = map[string]AuthFile{
		"auth-a": {AuthIndex: "auth-a", Name: "shared.json", Path: sharedPath, JSON: modified},
		"auth-b": {AuthIndex: "auth-b", Name: "shared.json", Path: sharedPath, JSON: modified},
	}
	host.saveFn = func(_ context.Context, file AuthFile) error {
		fields, errDecode := decodeAuthFileObject(file.JSON)
		if errDecode != nil {
			return errDecode
		}
		priority := priorityFromRawJSON(fields["priority"])
		host.mu.Lock()
		defer host.mu.Unlock()
		for _, authIndex := range []string{"auth-a", "auth-b"} {
			shared := host.files[authIndex]
			shared.JSON = append(json.RawMessage(nil), file.JSON...)
			host.files[authIndex] = shared
			runtimeAuth := host.runtime[authIndex]
			runtimeAuth.Priority = priority
			host.runtime[authIndex] = runtimeAuth
		}
		host.saved = append(host.saved, file)
		return nil
	}

	service := NewService(host)
	cfg, _ := ParseConfig([]byte("priority_sync_timeout: 500ms\n"))
	changed, errRecover := service.recoverStalePriorityOverrides(context.Background(), cfg, host.auths)
	if errRecover != nil || !changed {
		t.Fatalf("recoverStalePriorityOverrides() changed=%v error=%v", changed, errRecover)
	}
	host.mu.Lock()
	saveCount := len(host.saved)
	file := host.files["auth-a"]
	host.mu.Unlock()
	if saveCount != 1 {
		t.Fatalf("SaveAuthFile calls = %d, want 1 per physical file", saveCount)
	}
	fields, _ := decodeAuthFileObject(file.JSON)
	if priorityFromRawJSON(fields["priority"]) != 1 {
		t.Fatalf("restored priority = %d, want 1", priorityFromRawJSON(fields["priority"]))
	}
	if _, present := fields[priorityOverrideMarkerKey]; present {
		t.Fatal("priority marker remains after shared-file recovery")
	}
}

func TestStaleRecoveryContinuesAfterMalformedMarker(t *testing.T) {
	host := newReadyFakeHost()
	goodModified, _, errBuild := buildPriorityOverrideJSON(json.RawMessage(`{"type":"codex","priority":2}`), "auth-good", "crashed-run", 10)
	if errBuild != nil {
		t.Fatalf("buildPriorityOverrideJSON() error = %v", errBuild)
	}
	badAuth := Auth{ID: "id-bad", AuthIndex: "auth-bad", Provider: "codex", Source: "file", Priority: 10}
	goodAuth := Auth{ID: "id-good", AuthIndex: "auth-good", Provider: "codex", Source: "file", Priority: 10}
	host.auths = []Auth{badAuth, goodAuth}
	host.runtime = map[string]Auth{"auth-bad": badAuth, "auth-good": goodAuth}
	host.files = map[string]AuthFile{
		"auth-bad":  {AuthIndex: "auth-bad", Name: "bad.json", Path: `C:\auth\bad.json`, JSON: json.RawMessage(`{"type":"codex","priority":10,"_cpa_auto_refresh_quota_priority_override":{}}`)},
		"auth-good": {AuthIndex: "auth-good", Name: "good.json", Path: `C:\auth\good.json`, JSON: goodModified},
	}

	service := NewService(host)
	cfg, _ := ParseConfig([]byte("priority_sync_timeout: 500ms\n"))
	changed, errRecover := service.recoverStalePriorityOverrides(context.Background(), cfg, host.auths)
	if !changed || errRecover == nil {
		t.Fatalf("recoverStalePriorityOverrides() changed=%v error=%v", changed, errRecover)
	}
	host.mu.Lock()
	goodFile := host.files["auth-good"]
	host.mu.Unlock()
	fields, _ := decodeAuthFileObject(goodFile.JSON)
	if priorityFromRawJSON(fields["priority"]) != 2 {
		t.Fatalf("valid marker was not recovered after malformed peer: %#v", fields)
	}
	if _, present := fields[priorityOverrideMarkerKey]; present {
		t.Fatal("valid marker remains after recovery")
	}
}
