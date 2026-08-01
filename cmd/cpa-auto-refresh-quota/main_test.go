package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/PeacherMZ/cpa-auto-refresh-quota/internal/refreshquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestABIHostSaveAuthFileWritesValidatedPhysicalPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	original := []byte(`{"type":"codex","priority":1}`)
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("write fixture: %v", errWrite)
	}
	host := &abiHost{}
	file := refreshquota.AuthFile{
		AuthIndex:      "auth-a",
		Name:           "auth.json",
		Path:           path,
		JSON:           json.RawMessage(`{"type":"codex","priority":10}`),
		ExpectedSHA256: authFileSHA256(original),
	}
	if errSave := host.SaveAuthFile(context.Background(), file); errSave != nil {
		t.Fatalf("SaveAuthFile() error = %v", errSave)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read saved file: %v", errRead)
	}
	if string(raw) != string(file.JSON) {
		t.Fatalf("saved JSON = %s, want %s", raw, file.JSON)
	}
	info, errInfo := os.Stat(path)
	if errInfo != nil {
		t.Fatalf("stat saved file: %v", errInfo)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode = %o, want 600", info.Mode().Perm())
	}
	if errSave := host.SaveAuthFile(context.Background(), file); !errors.Is(errSave, refreshquota.ErrAuthFileChanged) {
		t.Fatalf("SaveAuthFile() with stale digest error = %v, want ErrAuthFileChanged", errSave)
	}

	file.ExpectedSHA256 = authFileSHA256(raw)
	file.Name = "other.json"
	if errSave := host.SaveAuthFile(context.Background(), file); errSave == nil {
		t.Fatal("SaveAuthFile() accepted mismatched name and path")
	}
}

func TestABIHostGetAuthFileUsesExactPhysicalBytesForCAS(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	physical := []byte("{\n  \"type\": \"codex\",\n  \"priority\": 1\n}\n")
	if errWrite := os.WriteFile(path, physical, 0o600); errWrite != nil {
		t.Fatalf("write fixture: %v", errWrite)
	}
	host := &abiHost{callOverride: func(_ context.Context, method string, _ any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostAuthGet {
			t.Fatalf("host callback method = %q, want %q", method, pluginabi.MethodHostAuthGet)
		}
		return json.Marshal(pluginapi.HostAuthGetResponse{
			AuthIndex: "auth-a",
			Name:      "auth.json",
			Path:      path,
			JSON:      json.RawMessage(`{"type":"codex","priority":1}`),
		})
	}}
	file, errGet := host.GetAuthFile(context.Background(), "auth-a")
	if errGet != nil {
		t.Fatalf("GetAuthFile() error = %v", errGet)
	}
	if string(file.JSON) != string(physical) {
		t.Fatalf("GetAuthFile().JSON = %q, want exact physical bytes %q", file.JSON, physical)
	}
	if file.ExpectedSHA256 != authFileSHA256(physical) {
		t.Fatalf("ExpectedSHA256 = %q, want physical file digest", file.ExpectedSHA256)
	}
	file.JSON = json.RawMessage(`{"type":"codex","priority":10}`)
	if errSave := host.SaveAuthFile(context.Background(), file); errSave != nil {
		t.Fatalf("SaveAuthFile() after ABI round trip error = %v", errSave)
	}
}

func TestABIHostSaveAuthFileRejectsUnsafeTargets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	original := []byte(`{"type":"codex","priority":1}`)
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("write fixture: %v", errWrite)
	}
	base := refreshquota.AuthFile{
		AuthIndex:      "auth-a",
		Name:           "auth.json",
		Path:           path,
		JSON:           json.RawMessage(`{"type":"codex","priority":10}`),
		ExpectedSHA256: authFileSHA256(original),
	}
	host := &abiHost{}
	for _, testCase := range []struct {
		name   string
		mutate func(*refreshquota.AuthFile)
	}{
		{name: "relative path", mutate: func(file *refreshquota.AuthFile) { file.Path = "auth.json" }},
		{name: "wrong extension", mutate: func(file *refreshquota.AuthFile) { file.Path = filepath.Join(dir, "auth.txt"); file.Name = "auth.txt" }},
		{name: "missing digest", mutate: func(file *refreshquota.AuthFile) { file.ExpectedSHA256 = "" }},
		{name: "missing auth index", mutate: func(file *refreshquota.AuthFile) { file.AuthIndex = "" }},
		{name: "non object JSON", mutate: func(file *refreshquota.AuthFile) { file.JSON = json.RawMessage(`[]`) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			file := base
			testCase.mutate(&file)
			if errSave := host.SaveAuthFile(context.Background(), file); errSave == nil {
				t.Fatal("SaveAuthFile() accepted unsafe target")
			}
		})
	}
}

func TestPluginRegistrationIsValidAndProvidesScheduler(t *testing.T) {
	registration := pluginRegistration()
	if registration.SchemaVersion != pluginSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", registration.SchemaVersion, pluginSchemaVersion)
	}
	if registration.Metadata.Name == "" || registration.Metadata.Version == "" || registration.Metadata.Author == "" || registration.Metadata.GitHubRepository == "" {
		t.Fatalf("required metadata is incomplete: %#v", registration.Metadata)
	}
	if !registration.Capabilities.ManagementAPI {
		t.Fatal("ManagementAPI capability = false")
	}
	if !registration.Capabilities.Scheduler {
		t.Fatal("Scheduler capability = false")
	}
}

func TestPluginRegistrationAlwaysUsesSchemaOne(t *testing.T) {
	for _, testCase := range []struct {
		name string
		host uint32
	}{
		{name: "legacy missing version", host: 0},
		{name: "schema one", host: 1},
		{name: "schema two", host: 2},
		{name: "future schema", host: pluginabi.SchemaVersion + 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			rawRequest, errMarshal := json.Marshal(lifecycleRequest{SchemaVersion: testCase.host})
			if errMarshal != nil {
				t.Fatalf("marshal lifecycle request: %v", errMarshal)
			}
			rawResponse, errHandle := handleMethod(pluginabi.MethodPluginReconfigure, rawRequest)
			if errHandle != nil {
				t.Fatalf("handleMethod(plugin.reconfigure) error = %v", errHandle)
			}
			response := decodeEnvelopeResult[registration](t, rawResponse)
			if response.SchemaVersion != pluginSchemaVersion || !response.Capabilities.Scheduler || !response.Capabilities.ManagementAPI {
				t.Fatalf("registration for host schema %d = %#v", testCase.host, response)
			}
		})
	}
}

func TestManagementRegistrationUsesAuthenticatedRoutesOnly(t *testing.T) {
	raw, errHandle := handleMethod(pluginabi.MethodManagementRegister, nil)
	if errHandle != nil {
		t.Fatalf("handleMethod(management.register) error = %v", errHandle)
	}
	result := decodeEnvelopeResult[managementRegistration](t, raw)
	if len(result.Routes) != 2 {
		t.Fatalf("routes = %#v, want two", result.Routes)
	}
	got := map[string]string{}
	for _, route := range result.Routes {
		if route.Menu != "" || route.Description != "" {
			t.Fatalf("route unexpectedly declares legacy resource metadata: %#v", route)
		}
		got[route.Method] = route.Path
	}
	if got[http.MethodGet] != statusRoute || got[http.MethodPost] != runRoute {
		t.Fatalf("registered routes = %#v", got)
	}
}

func TestManagementStatusAndStrictRunRequest(t *testing.T) {
	statusRequest, errMarshal := json.Marshal(managementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management" + statusRoute,
	}})
	if errMarshal != nil {
		t.Fatalf("marshal status request: %v", errMarshal)
	}
	raw, errHandle := handleMethod(pluginabi.MethodManagementHandle, statusRequest)
	if errHandle != nil {
		t.Fatalf("status management handle error = %v", errHandle)
	}
	statusResponse := decodeEnvelopeResult[pluginapi.ManagementResponse](t, raw)
	if statusResponse.StatusCode != http.StatusOK || statusResponse.Headers.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("status response = %#v", statusResponse)
	}
	if !json.Valid(statusResponse.Body) {
		t.Fatalf("status body is not JSON: %q", statusResponse.Body)
	}

	runRequest, errMarshal := json.Marshal(managementRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management" + runRoute,
		Body:   []byte(`{"unknown":true}`),
	}})
	if errMarshal != nil {
		t.Fatalf("marshal run request: %v", errMarshal)
	}
	raw, errHandle = handleMethod(pluginabi.MethodManagementHandle, runRequest)
	if errHandle != nil {
		t.Fatalf("run management handle error = %v", errHandle)
	}
	runResponse := decodeEnvelopeResult[pluginapi.ManagementResponse](t, raw)
	if runResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("strict run response status = %d, want 400; body=%s", runResponse.StatusCode, runResponse.Body)
	}
}

func TestNormalizeManualRunOptionsRejectsEmptyAndDeduplicates(t *testing.T) {
	request := manualRunRequest{DryRun: true, AuthIndices: json.RawMessage(`[" auth-a ","auth-a","auth-b"]`)}
	options, errRequest := request.options()
	if errRequest != nil {
		t.Fatalf("manualRunRequest.options() error = %v", errRequest)
	}
	if err := normalizeManualRunOptions(&options); err != nil {
		t.Fatalf("normalizeManualRunOptions() error = %v", err)
	}
	if !options.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if want := []string{"auth-a", "auth-b"}; !reflect.DeepEqual(options.AuthIndices, want) {
		t.Fatalf("AuthIndices = %#v, want %#v", options.AuthIndices, want)
	}

	for _, raw := range []string{"null", "[]", `[""]`, `["   "]`, `["auth-a","\t"]`} {
		request = manualRunRequest{AuthIndices: json.RawMessage(raw)}
		options, errRequest = request.options()
		if errRequest == nil {
			errRequest = normalizeManualRunOptions(&options)
		}
		if errRequest == nil {
			t.Fatalf("manual run auth_indices %s error = nil", raw)
		}
	}

	options, errRequest = (manualRunRequest{}).options()
	if errRequest != nil || len(options.AuthIndices) != 0 {
		t.Fatalf("omitted auth_indices options = %#v, error = %v", options, errRequest)
	}
}

func decodeEnvelopeResult[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env envelope
	if errDecode := json.Unmarshal(raw, &env); errDecode != nil {
		t.Fatalf("decode envelope: %v", errDecode)
	}
	if !env.OK || env.Error != nil {
		t.Fatalf("envelope failed: %s", raw)
	}
	var result T
	if errDecode := json.Unmarshal(env.Result, &result); errDecode != nil {
		t.Fatalf("decode envelope result: %v", errDecode)
	}
	return result
}
