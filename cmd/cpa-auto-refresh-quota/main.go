package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"unsafe"

	"github.com/PeacherMZ/cpa-auto-refresh-quota/internal/refreshquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID               = "cpa-auto-refresh-quota"
	pluginSchemaVersion    = uint32(1)
	statusRoute            = "/plugins/cpa-auto-refresh-quota/status"
	runRoute               = "/plugins/cpa-auto-refresh-quota/run"
	maxManagementBodyBytes = 64 * 1024
	maxAuthFileWriteBytes  = 4 * 1024 * 1024
)

// pluginVersion 可在发布构建时通过 -ldflags "-X main.pluginVersion=..." 注入。
var pluginVersion = "0.3.0"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
	Scheduler     bool `json:"scheduler"`
}

type managementRegistration struct {
	Routes []pluginapi.ManagementRoute `json:"routes,omitempty"`
}

type managementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type manualRunRequest struct {
	DryRun      bool            `json:"dry_run"`
	AuthIndices json.RawMessage `json:"auth_indices"`
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type hostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type abiHost struct {
	mu           sync.Mutex
	pins         *authPinRegistry
	callOverride func(context.Context, string, any) (json.RawMessage, error)
}

var (
	hostBridge       = &abiHost{pins: newAuthPinRegistry()}
	serviceLifecycle sync.RWMutex
	runtimeService   = refreshquota.NewService(hostBridge)
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	hostBridge.mu.Lock()
	C.store_host_api(host)
	hostBridge.mu.Unlock()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	shutdownService()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var lifecycle lifecycleRequest
		if len(request) > 0 {
			if errDecode := json.Unmarshal(request, &lifecycle); errDecode != nil {
				return nil, fmt.Errorf("decode lifecycle request: %w", errDecode)
			}
		}
		if errApply := applyConfiguration(lifecycle.ConfigYAML); errApply != nil {
			return nil, errApply
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginShutdown:
		shutdownService()
		return okEnvelope(map[string]any{})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{Routes: []pluginapi.ManagementRoute{
			{Method: http.MethodGet, Path: statusRoute},
			{Method: http.MethodPost, Path: runRoute},
		}})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	case pluginabi.MethodSchedulerPick:
		var schedulerRequest pluginapi.SchedulerPickRequest
		if errDecode := json.Unmarshal(request, &schedulerRequest); errDecode != nil {
			return nil, fmt.Errorf("decode scheduler request: %w", errDecode)
		}
		response, errPick := pickPinnedAuth(hostBridge.pins, schedulerRequest)
		if errPick != nil {
			code := "scheduler_pin_failed"
			switch {
			case errors.Is(errPick, errPinnedAuthUnavailable):
				code = "target_auth_unavailable"
			case errors.Is(errPick, errInvalidPinHeader):
				code = "invalid_routing_token"
			}
			return errorEnvelope(code, errPick.Error()), nil
		}
		return okEnvelope(response)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginSchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          pluginVersion,
			Author:           "cpa-auto-refresh-quota contributors",
			GitHubRepository: "https://github.com/PeacherMZ/cpa-auto-refresh-quota",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "schedule_enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否启用每日定时任务。"},
				{Name: "timezone", Type: pluginapi.ConfigFieldTypeString, Description: "IANA 时区，例如 Asia/Shanghai。"},
				{Name: "times", Type: pluginapi.ConfigFieldTypeArray, Description: "每日执行时间，格式为 HH:MM 或 HH:MM:SS。"},
				{Name: "model", Type: pluginapi.ConfigFieldTypeString, Description: "通过 CPA 调用的模型。"},
				{Name: "message", Type: pluginapi.ConfigFieldTypeString, Description: "发送给每个目标认证的消息。"},
				{Name: "entry_protocol", Type: pluginapi.ConfigFieldTypeString, Description: "当前必须为 openai；插件会构造 Chat Completions 请求。"},
				{Name: "exit_protocol", Type: pluginapi.ConfigFieldTypeString, Description: "宿主响应协议，通常为 openai，也可使用 CPA 支持的转换格式。"},
				{Name: "max_tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "最大响应 token 数。"},
				{Name: "providers", Type: pluginapi.ConfigFieldTypeArray, Description: "可选的 provider 白名单。"},
				{Name: "include_auth_indices", Type: pluginapi.ConfigFieldTypeArray, Description: "可选的认证索引白名单。"},
				{Name: "exclude_auth_indices", Type: pluginapi.ConfigFieldTypeArray, Description: "可选的认证索引排除列表。"},
				{Name: "physical_files_only", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否只使用由物理认证文件支持的记录。"},
				{Name: "skip_unavailable", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否跳过被标记为不可用的认证。"},
				{Name: "temporary_priority_override", Type: pluginapi.ConfigFieldTypeBoolean, Description: "是否临时把目标认证的优先级对齐到全局最高档。"},
				{Name: "priority_sync_timeout", Type: pluginapi.ConfigFieldTypeString, Description: "等待 CPA 发布认证优先级的最长时间，例如 5s。"},
				{Name: "request_timeout", Type: pluginapi.ConfigFieldTypeString, Description: "单个认证模型请求的最长时间，例如 2m。"},
				{Name: "delay_between_auths", Type: pluginapi.ConfigFieldTypeString, Description: "相邻认证调用之间的间隔，例如 1s。"},
			},
		},
		Capabilities: registrationCapabilities{ManagementAPI: true, Scheduler: true},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return nil, fmt.Errorf("decode management request: %w", errDecode)
	}

	switch {
	case req.Method == http.MethodGet && req.Path == "/v0/management"+statusRoute:
		return okEnvelope(jsonManagementResponse(http.StatusOK, currentService().Status()))
	case req.Method == http.MethodPost && req.Path == "/v0/management"+runRoute:
		if len(req.Body) > maxManagementBodyBytes {
			return okEnvelope(jsonManagementResponse(http.StatusRequestEntityTooLarge, map[string]any{"error": "request body is too large"}))
		}
		request := manualRunRequest{}
		if len(bytes.TrimSpace(req.Body)) > 0 {
			if errDecode := decodeStrictJSON(req.Body, &request); errDecode != nil {
				return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": errDecode.Error()}))
			}
		}
		options, errRequest := request.options()
		if errRequest != nil {
			return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": errRequest.Error()}))
		}
		if errOptions := normalizeManualRunOptions(&options); errOptions != nil {
			return okEnvelope(jsonManagementResponse(http.StatusBadRequest, map[string]any{"error": errOptions.Error()}))
		}
		runID, errStart := currentService().StartManual(options)
		if errStart != nil {
			statusCode := http.StatusInternalServerError
			switch {
			case errors.Is(errStart, refreshquota.ErrBusy):
				statusCode = http.StatusConflict
			case errors.Is(errStart, refreshquota.ErrNotConfigured):
				statusCode = http.StatusBadRequest
			case errors.Is(errStart, refreshquota.ErrStopped):
				statusCode = http.StatusServiceUnavailable
			}
			return okEnvelope(jsonManagementResponse(statusCode, map[string]any{"error": errStart.Error()}))
		}
		return okEnvelope(jsonManagementResponse(http.StatusAccepted, map[string]any{"accepted": true, "run_id": runID}))
	default:
		return okEnvelope(jsonManagementResponse(http.StatusNotFound, map[string]any{"error": "route not found"}))
	}
}

func (request manualRunRequest) options() (refreshquota.ManualRunOptions, error) {
	options := refreshquota.ManualRunOptions{DryRun: request.DryRun}
	rawAuthIndices := bytes.TrimSpace(request.AuthIndices)
	if len(rawAuthIndices) == 0 {
		return options, nil
	}
	if bytes.Equal(rawAuthIndices, []byte("null")) {
		return refreshquota.ManualRunOptions{}, fmt.Errorf("auth_indices must be a non-empty array when provided")
	}
	if err := json.Unmarshal(rawAuthIndices, &options.AuthIndices); err != nil {
		return refreshquota.ManualRunOptions{}, fmt.Errorf("auth_indices must be an array of strings: %w", err)
	}
	if len(options.AuthIndices) == 0 {
		return refreshquota.ManualRunOptions{}, fmt.Errorf("auth_indices must be a non-empty array when provided")
	}
	return options, nil
}

func normalizeManualRunOptions(options *refreshquota.ManualRunOptions) error {
	if options == nil {
		return fmt.Errorf("manual run options are required")
	}
	if len(options.AuthIndices) > 256 {
		return fmt.Errorf("auth_indices contains more than 256 entries")
	}
	normalized := make([]string, 0, len(options.AuthIndices))
	seen := make(map[string]struct{}, len(options.AuthIndices))
	for _, authIndex := range options.AuthIndices {
		authIndex = strings.TrimSpace(authIndex)
		if authIndex == "" {
			return fmt.Errorf("auth_indices must not contain empty values")
		}
		if _, exists := seen[authIndex]; exists {
			continue
		}
		seen[authIndex] = struct{}{}
		normalized = append(normalized, authIndex)
	}
	options.AuthIndices = normalized
	return nil
}

func currentService() *refreshquota.Service {
	serviceLifecycle.RLock()
	service := runtimeService
	serviceLifecycle.RUnlock()
	return service
}

func applyConfiguration(raw []byte) error {
	serviceLifecycle.Lock()
	defer serviceLifecycle.Unlock()
	if runtimeService == nil || runtimeService.Status().Stopped {
		runtimeService = refreshquota.NewService(hostBridge)
	}
	return runtimeService.ApplyConfig(raw)
}

func shutdownService() {
	serviceLifecycle.Lock()
	defer serviceLifecycle.Unlock()
	if runtimeService != nil {
		runtimeService.Shutdown()
	}
}

func decodeStrictJSON(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if errDecode := decoder.Decode(dst); errDecode != nil {
		return fmt.Errorf("invalid JSON request: %w", errDecode)
	}
	if errTrailing := decoder.Decode(&struct{}{}); !errors.Is(errTrailing, io.EOF) {
		if errTrailing == nil {
			return fmt.Errorf("invalid JSON request: multiple values")
		}
		return fmt.Errorf("invalid JSON request: %w", errTrailing)
	}
	return nil
}

func jsonManagementResponse(statusCode int, value any) pluginapi.ManagementResponse {
	body, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		statusCode = http.StatusInternalServerError
		body = []byte(`{"error":"encode response"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: statusCode,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func (h *abiHost) ListAuths(ctx context.Context) ([]refreshquota.Auth, error) {
	result, errCall := h.call(ctx, pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return nil, errCall
	}
	var response authListResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		return nil, fmt.Errorf("decode host.auth.list result: %w", errDecode)
	}
	auths := make([]refreshquota.Auth, 0, len(response.Files))
	for _, item := range response.Files {
		auths = append(auths, authFromHost(item))
	}
	return auths, nil
}

func (h *abiHost) GetRuntimeAuth(ctx context.Context, authIndex string) (refreshquota.Auth, error) {
	result, errCall := h.call(ctx, pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return refreshquota.Auth{}, errCall
	}
	var response pluginapi.HostAuthGetRuntimeResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		return refreshquota.Auth{}, fmt.Errorf("decode host.auth.get_runtime result: %w", errDecode)
	}
	return authFromHost(response.Auth), nil
}

func (h *abiHost) GetAuthFile(ctx context.Context, authIndex string) (refreshquota.AuthFile, error) {
	result, errCall := h.call(ctx, pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return refreshquota.AuthFile{}, errCall
	}
	var response pluginapi.HostAuthGetResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		return refreshquota.AuthFile{}, fmt.Errorf("decode host.auth.get result: %w", errDecode)
	}
	if strings.TrimSpace(response.AuthIndex) != strings.TrimSpace(authIndex) {
		return refreshquota.AuthFile{}, fmt.Errorf("host.auth.get returned a different auth_index")
	}
	path, info, errPath := validatePhysicalAuthFile(response.Name, response.Path)
	if errPath != nil {
		return refreshquota.AuthFile{}, errPath
	}
	if info.Size() > maxAuthFileWriteBytes {
		return refreshquota.AuthFile{}, fmt.Errorf("auth file is too large")
	}
	if errContext := ctx.Err(); errContext != nil {
		return refreshquota.AuthFile{}, errContext
	}
	raw, errRead := readAuthFileBounded(path)
	if errRead != nil {
		return refreshquota.AuthFile{}, fmt.Errorf("read physical auth file: %w", errRead)
	}
	if errJSON := validateAuthFileJSON(raw); errJSON != nil {
		return refreshquota.AuthFile{}, fmt.Errorf("physical auth file JSON is invalid: %w", errJSON)
	}
	return refreshquota.AuthFile{
		AuthIndex:      response.AuthIndex,
		Name:           strings.TrimSpace(response.Name),
		Path:           path,
		JSON:           append(json.RawMessage(nil), raw...),
		ExpectedSHA256: authFileSHA256(raw),
	}, nil
}

func (h *abiHost) SaveAuthFile(ctx context.Context, file refreshquota.AuthFile) error {
	if strings.TrimSpace(file.AuthIndex) == "" {
		return fmt.Errorf("auth_index is required")
	}
	name := strings.TrimSpace(file.Name)
	if name == "" {
		return fmt.Errorf("auth file name is required")
	}
	if len(bytes.TrimSpace(file.JSON)) == 0 {
		return fmt.Errorf("auth file JSON is required")
	}
	if len(file.JSON) > maxAuthFileWriteBytes {
		return fmt.Errorf("auth file JSON is too large")
	}
	if errJSON := validateAuthFileJSON(file.JSON); errJSON != nil {
		return fmt.Errorf("auth file JSON is invalid: %w", errJSON)
	}
	path, info, errPath := validatePhysicalAuthFile(name, file.Path)
	if errPath != nil {
		return errPath
	}
	current, errRead := readAuthFileBounded(path)
	if errRead != nil {
		return fmt.Errorf("read auth file before write: %w", errRead)
	}
	if strings.TrimSpace(file.ExpectedSHA256) == "" || !strings.EqualFold(file.ExpectedSHA256, authFileSHA256(current)) {
		return fmt.Errorf("%w", refreshquota.ErrAuthFileChanged)
	}
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	if errWrite := atomicWriteAuthFile(ctx, path, file.JSON, info.Mode().Perm(), file.ExpectedSHA256); errWrite != nil {
		return fmt.Errorf("write auth file: %w", errWrite)
	}
	return nil
}

func validatePhysicalAuthFile(name, path string) (string, os.FileInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, fmt.Errorf("auth file name is required")
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || !filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".json") || !sameAuthFileName(filepath.Base(path), name) {
		return "", nil, fmt.Errorf("auth file path is invalid")
	}
	info, errInfo := os.Lstat(path)
	if errInfo != nil {
		return "", nil, fmt.Errorf("inspect auth file: %w", errInfo)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("auth file is not a regular file")
	}
	return path, info, nil
}

func sameAuthFileName(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func authFileSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validateAuthFileJSON(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("JSON is empty")
	}
	var object map[string]json.RawMessage
	if errDecode := json.Unmarshal(raw, &object); errDecode != nil {
		return errDecode
	}
	if object == nil {
		return fmt.Errorf("top-level JSON value must be an object")
	}
	return nil
}

func readAuthFileBounded(path string) ([]byte, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return nil, errOpen
	}
	defer file.Close()
	info, errInfo := file.Stat()
	if errInfo != nil {
		return nil, errInfo
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("auth file is not a regular file")
	}
	if info.Size() > maxAuthFileWriteBytes {
		return nil, fmt.Errorf("auth file exceeds %d bytes", maxAuthFileWriteBytes)
	}
	raw, errRead := io.ReadAll(io.LimitReader(file, maxAuthFileWriteBytes+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(raw) > maxAuthFileWriteBytes {
		return nil, fmt.Errorf("auth file exceeds %d bytes", maxAuthFileWriteBytes)
	}
	return raw, nil
}

func atomicWriteAuthFile(ctx context.Context, path string, raw []byte, mode os.FileMode, expectedSHA256 string) (err error) {
	temp, errCreate := os.CreateTemp(filepath.Dir(path), ".cpa-auto-refresh-*.tmp")
	if errCreate != nil {
		return errCreate
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if _, errWrite := temp.Write(raw); errWrite != nil {
		return errWrite
	}
	if errChmod := temp.Chmod(mode); errChmod != nil {
		return errChmod
	}
	if errSync := temp.Sync(); errSync != nil {
		return errSync
	}
	if errClose := temp.Close(); errClose != nil {
		return errClose
	}
	if errReplace := replaceAuthFileCAS(ctx, path, tempPath, expectedSHA256); errReplace != nil {
		return errReplace
	}
	return nil
}

func (h *abiHost) ExecutePinned(ctx context.Context, req refreshquota.PinnedModelRequest) (refreshquota.ModelResponse, error) {
	if h == nil || h.pins == nil {
		return refreshquota.ModelResponse{}, fmt.Errorf("auth pin registry is unavailable")
	}
	token, errRegister := h.pins.register(req.AuthID)
	if errRegister != nil {
		return refreshquota.ModelResponse{}, errRegister
	}
	headers := cloneHeader(req.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Del(pinHeaderName)
	headers.Set(pinHeaderName, token)

	result, errCall := h.call(ctx, pluginabi.MethodHostModelExecute, pluginapi.HostModelExecutionRequest{
		EntryProtocol: req.EntryProtocol,
		ExitProtocol:  req.ExitProtocol,
		Model:         req.Model,
		Stream:        false,
		Body:          append([]byte(nil), req.Body...),
		Headers:       headers,
		Query:         cloneValues(req.Query),
		Alt:           req.Alt,
	})
	state, exists := h.pins.finish(token)
	if !exists || !state.Selected {
		if state.SchedulerSeen && errCall != nil {
			return refreshquota.ModelResponse{}, errCall
		}
		return refreshquota.ModelResponse{}, errPinNotSelected
	}
	if errCall != nil {
		return refreshquota.ModelResponse{}, errCall
	}
	var response pluginapi.HostModelExecutionResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		return refreshquota.ModelResponse{}, fmt.Errorf("decode host.model.execute result: %w", errDecode)
	}
	return refreshquota.ModelResponse{
		StatusCode: response.StatusCode,
		Headers:    cloneHeader(response.Headers),
		Body:       append([]byte(nil), response.Body...),
	}, nil
}

func (h *abiHost) Log(ctx context.Context, level, message string, fields map[string]any) error {
	_, errCall := h.call(ctx, pluginabi.MethodHostLog, hostLogRequest{
		Level:   strings.TrimSpace(level),
		Message: strings.TrimSpace(message),
		Fields:  fields,
	})
	return errCall
}

func (h *abiHost) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}
	if h.callOverride != nil {
		return h.callOverride(ctx, method, payload)
	}
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if errContext := ctx.Err(); errContext != nil {
		return nil, errContext
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env envelope
	if errDecode := json.Unmarshal(rawResponse, &env); errDecode != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errDecode)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func authFromHost(item pluginapi.HostAuthFileEntry) refreshquota.Auth {
	return refreshquota.Auth{
		ID:          item.ID,
		AuthIndex:   item.AuthIndex,
		Provider:    item.Provider,
		Status:      item.Status,
		Disabled:    item.Disabled,
		Unavailable: item.Unavailable,
		RuntimeOnly: item.RuntimeOnly,
		Source:      item.Source,
		Priority:    item.Priority,
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}
