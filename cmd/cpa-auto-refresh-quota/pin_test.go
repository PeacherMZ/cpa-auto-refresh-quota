package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/PeacherMZ/cpa-auto-refresh-quota/internal/refreshquota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestSchedulerPickSelectsRegisteredAuthAndIgnoresNormalRequests(t *testing.T) {
	registry := newAuthPinRegistry()

	normalResponse, errNormal := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
		Options:    pluginapi.SchedulerOptions{Headers: map[string][]string{"X-Other": {"value"}}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a"}},
	})
	if errNormal != nil || normalResponse.Handled {
		t.Fatalf("normal scheduler response = %#v, error = %v", normalResponse, errNormal)
	}

	token, errRegister := registry.register("auth-b")
	if errRegister != nil {
		t.Fatalf("register() error = %v", errRegister)
	}
	response, errPick := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
		Options: pluginapi.SchedulerOptions{Headers: map[string][]string{
			stringsLower(pinHeaderName): {token},
		}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a"}, {ID: "auth-b"}},
	})
	if errPick != nil {
		t.Fatalf("pickPinnedAuth() error = %v", errPick)
	}
	if !response.Handled || response.AuthID != "auth-b" {
		t.Fatalf("scheduler response = %#v", response)
	}
	state, exists := registry.finish(token)
	if !exists || !state.SchedulerSeen || !state.Selected || state.AuthID != "auth-b" {
		t.Fatalf("pin state = %#v, exists = %v", state, exists)
	}
}

func TestSchedulerPickFailsClosedForReservedHeaderProblems(t *testing.T) {
	registry := newAuthPinRegistry()

	if _, errPick := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
		Options: pluginapi.SchedulerOptions{Headers: map[string][]string{pinHeaderName: {"unknown"}}},
	}); !errors.Is(errPick, errInvalidPinHeader) {
		t.Fatalf("unknown token error = %v, want errInvalidPinHeader", errPick)
	}

	token, errRegister := registry.register("auth-a")
	if errRegister != nil {
		t.Fatalf("register() error = %v", errRegister)
	}
	if _, errPick := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
		Options:    pluginapi.SchedulerOptions{Headers: map[string][]string{pinHeaderName: {token}}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-b"}},
	}); !errors.Is(errPick, errPinnedAuthUnavailable) {
		t.Fatalf("missing target error = %v, want errPinnedAuthUnavailable", errPick)
	}
	state, exists := registry.finish(token)
	if !exists || !state.SchedulerSeen || state.Selected {
		t.Fatalf("missing-target pin state = %#v, exists = %v", state, exists)
	}
}

func TestSchedulerABIUsesErrorEnvelopeForMissingTarget(t *testing.T) {
	token, errRegister := hostBridge.pins.register("auth-required")
	if errRegister != nil {
		t.Fatalf("register() error = %v", errRegister)
	}
	t.Cleanup(func() { hostBridge.pins.finish(token) })

	rawRequest, errMarshal := json.Marshal(pluginapi.SchedulerPickRequest{
		Options:    pluginapi.SchedulerOptions{Headers: map[string][]string{pinHeaderName: {token}}},
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-other"}},
	})
	if errMarshal != nil {
		t.Fatalf("marshal scheduler request: %v", errMarshal)
	}
	rawResponse, errHandle := handleMethod(pluginabi.MethodSchedulerPick, rawRequest)
	if errHandle != nil {
		t.Fatalf("handleMethod() error = %v", errHandle)
	}
	var env envelope
	if errDecode := json.Unmarshal(rawResponse, &env); errDecode != nil {
		t.Fatalf("decode scheduler envelope: %v", errDecode)
	}
	if env.OK || env.Error == nil || env.Error.Code != "target_auth_unavailable" {
		t.Fatalf("scheduler envelope = %s", rawResponse)
	}
}

func TestExecutePinnedUsesStockHostModelCallbackAndCleansToken(t *testing.T) {
	registry := newAuthPinRegistry()
	host := &abiHost{pins: registry}
	host.callOverride = func(_ context.Context, method string, payload any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostModelExecute {
			return nil, fmt.Errorf("method = %s", method)
		}
		request, ok := payload.(pluginapi.HostModelExecutionRequest)
		if !ok {
			return nil, fmt.Errorf("payload type = %T", payload)
		}
		if request.Headers.Get("Authorization") != "" {
			return nil, fmt.Errorf("unexpected authorization header")
		}
		response, errPick := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
			Model:      request.Model,
			Options:    pluginapi.SchedulerOptions{Headers: request.Headers},
			Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "auth-a"}, {ID: "auth-b"}},
		})
		if errPick != nil {
			return nil, errPick
		}
		if !response.Handled || response.AuthID != "auth-b" {
			return nil, fmt.Errorf("scheduler response = %#v", response)
		}
		return json.Marshal(pluginapi.HostModelExecutionResponse{
			StatusCode: http.StatusOK,
			Headers:    http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"ok":true}`),
		})
	}

	response, errExecute := host.ExecutePinned(context.Background(), refreshquota.PinnedModelRequest{
		AuthID:        "auth-b",
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
		Body:          []byte(`{"model":"model-1"}`),
	})
	if errExecute != nil {
		t.Fatalf("ExecutePinned() error = %v", errExecute)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"ok":true}` {
		t.Fatalf("ExecutePinned() response = %#v", response)
	}
	if count := registryCount(registry); count != 0 {
		t.Fatalf("registry entries after ExecutePinned = %d", count)
	}
}

func TestExecutePinnedFailsWhenSchedulerWasNotInvoked(t *testing.T) {
	registry := newAuthPinRegistry()
	host := &abiHost{
		pins: registry,
		callOverride: func(context.Context, string, any) (json.RawMessage, error) {
			return json.Marshal(pluginapi.HostModelExecutionResponse{StatusCode: http.StatusOK})
		},
	}
	_, errExecute := host.ExecutePinned(context.Background(), refreshquota.PinnedModelRequest{
		AuthID:        "auth-a",
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Model:         "model-1",
	})
	if !errors.Is(errExecute, errPinNotSelected) {
		t.Fatalf("ExecutePinned() error = %v, want errPinNotSelected", errExecute)
	}
	if count := registryCount(registry); count != 0 {
		t.Fatalf("registry entries after failed ExecutePinned = %d", count)
	}
}

func TestAuthPinRegistryConcurrentSelectionsDoNotCross(t *testing.T) {
	registry := newAuthPinRegistry()
	const count = 64
	type pin struct {
		token  string
		authID string
	}
	pins := make([]pin, 0, count)
	for index := 0; index < count; index++ {
		authID := fmt.Sprintf("auth-%d", index)
		token, errRegister := registry.register(authID)
		if errRegister != nil {
			t.Fatalf("register(%s) error = %v", authID, errRegister)
		}
		pins = append(pins, pin{token: token, authID: authID})
	}

	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for _, item := range pins {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, errPick := pickPinnedAuth(registry, pluginapi.SchedulerPickRequest{
				Options:    pluginapi.SchedulerOptions{Headers: map[string][]string{pinHeaderName: {item.token}}},
				Candidates: []pluginapi.SchedulerAuthCandidate{{ID: item.authID}},
			})
			if errPick != nil {
				errorsCh <- errPick
				return
			}
			if !response.Handled || response.AuthID != item.authID {
				errorsCh <- fmt.Errorf("token for %s selected %#v", item.authID, response)
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for errPick := range errorsCh {
		t.Error(errPick)
	}
	for _, item := range pins {
		state, exists := registry.finish(item.token)
		if !exists || !state.Selected || state.AuthID != item.authID {
			t.Fatalf("pin %s state = %#v, exists = %v", item.authID, state, exists)
		}
	}
}

func registryCount(registry *authPinRegistry) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.pins)
}

func stringsLower(value string) string {
	result := make([]byte, len(value))
	for index := range value {
		character := value[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		result[index] = character
	}
	return string(result)
}
