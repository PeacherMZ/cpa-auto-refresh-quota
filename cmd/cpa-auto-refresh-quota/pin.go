package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pinHeaderName = "X-CPA-Auto-Refresh-Token"

var (
	errPinNotSelected        = errors.New("CPA scheduler did not select the requested auth")
	errPinnedAuthUnavailable = errors.New("requested auth is not available to the CPA scheduler")
	errInvalidPinHeader      = errors.New("invalid CPA auto-refresh routing token header")
)

type authPinState struct {
	AuthID        string
	SchedulerSeen bool
	Selected      bool
}

type authPinRegistry struct {
	mu   sync.Mutex
	pins map[string]authPinState
}

func newAuthPinRegistry() *authPinRegistry {
	return &authPinRegistry{pins: make(map[string]authPinState)}
}

func (r *authPinRegistry) register(authID string) (string, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "", fmt.Errorf("auth ID is required")
	}
	if r == nil {
		return "", fmt.Errorf("auth pin registry is unavailable")
	}

	for attempt := 0; attempt < 4; attempt++ {
		var raw [32]byte
		if _, errRead := rand.Read(raw[:]); errRead != nil {
			return "", fmt.Errorf("create routing token: %w", errRead)
		}
		token := hex.EncodeToString(raw[:])

		r.mu.Lock()
		if r.pins == nil {
			r.pins = make(map[string]authPinState)
		}
		if _, exists := r.pins[token]; !exists {
			r.pins[token] = authPinState{AuthID: authID}
			r.mu.Unlock()
			return token, nil
		}
		r.mu.Unlock()
	}
	return "", fmt.Errorf("create unique routing token")
}

func (r *authPinRegistry) finish(token string) (authPinState, bool) {
	if r == nil || token == "" {
		return authPinState{}, false
	}
	r.mu.Lock()
	state, exists := r.pins[token]
	delete(r.pins, token)
	r.mu.Unlock()
	return state, exists
}

func (r *authPinRegistry) matchHeader(headers map[string][]string) (string, authPinState, bool, error) {
	if r == nil {
		return "", authPinState{}, false, nil
	}
	values, present := pinHeaderValues(headers)
	if !present {
		return "", authPinState{}, false, nil
	}
	if len(values) == 0 {
		return "", authPinState{}, true, errInvalidPinHeader
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	known := make([]string, 0, 1)
	for _, value := range values {
		if _, exists := r.pins[value]; exists {
			known = append(known, value)
		}
	}
	if len(known) == 0 {
		return "", authPinState{}, true, errInvalidPinHeader
	}
	for _, token := range known {
		state := r.pins[token]
		state.SchedulerSeen = true
		r.pins[token] = state
	}
	if len(values) != 1 || len(known) != 1 {
		return "", authPinState{}, true, errInvalidPinHeader
	}
	token := known[0]
	return token, r.pins[token], true, nil
}

func (r *authPinRegistry) markSelected(token string) {
	if r == nil || token == "" {
		return
	}
	r.mu.Lock()
	state, exists := r.pins[token]
	if exists {
		state.SchedulerSeen = true
		state.Selected = true
		r.pins[token] = state
	}
	r.mu.Unlock()
}

func pinHeaderValues(headers map[string][]string) ([]string, bool) {
	values := make([]string, 0, 1)
	present := false
	for key, items := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), pinHeaderName) {
			continue
		}
		present = true
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item != "" {
				values = append(values, item)
			}
		}
	}
	return values, present
}

func pickPinnedAuth(registry *authPinRegistry, request pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	token, state, handled, errMatch := registry.matchHeader(request.Options.Headers)
	if errMatch != nil {
		return pluginapi.SchedulerPickResponse{}, errMatch
	}
	if !handled {
		return pluginapi.SchedulerPickResponse{}, nil
	}
	for _, candidate := range request.Candidates {
		if strings.TrimSpace(candidate.ID) == state.AuthID {
			registry.markSelected(token)
			return pluginapi.SchedulerPickResponse{AuthID: state.AuthID, Handled: true}, nil
		}
	}
	return pluginapi.SchedulerPickResponse{}, errPinnedAuthUnavailable
}
