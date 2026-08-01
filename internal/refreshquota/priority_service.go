package refreshquota

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const priorityPollInterval = 50 * time.Millisecond

func (s *Service) recoverStalePriorityOverrides(ctx context.Context, cfg Config, auths []Auth) (bool, error) {
	changed := false
	seenPaths := make(map[string]struct{})
	var recoveryErrors []error
	for _, auth := range auths {
		if errContext := ctx.Err(); errContext != nil {
			recoveryErrors = append(recoveryErrors, errContext)
			break
		}
		if auth.RuntimeOnly || !strings.EqualFold(strings.TrimSpace(auth.Source), "file") || strings.TrimSpace(auth.AuthIndex) == "" {
			continue
		}
		file, errGet := s.host.GetAuthFile(ctx, auth.AuthIndex)
		if errGet != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("inspect auth_index %s for stale priority override: %w", auth.AuthIndex, errGet))
			continue
		}
		pathKey := priorityRecoveryPathKey(file.Path)
		if pathKey != "" {
			if _, exists := seenPaths[pathKey]; exists {
				continue
			}
			seenPaths[pathKey] = struct{}{}
		}
		marker, present, errMarker := priorityOverrideMarkerFromJSON(file.JSON)
		if errMarker != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("inspect stale priority override for auth_index %s: %w", auth.AuthIndex, errMarker))
			continue
		}
		if !present {
			continue
		}
		externalChange, needsSave, errRestore := s.restorePriorityFile(ctx, cfg.PrioritySyncTimeout, auth.AuthIndex, auth.ID, &marker)
		if errRestore != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("restore stale priority override for auth_index %s: %w", marker.AuthIndex, errRestore))
			continue
		}
		changed = changed || needsSave
		level := "info"
		if externalChange {
			level = "warn"
		}
		s.log(level, "stale credential priority override recovered", map[string]any{
			"event":             "priority_override_recovered",
			"auth_index":        marker.AuthIndex,
			"access_auth_index": auth.AuthIndex,
			"external_change":   externalChange,
		})
	}
	return changed, errors.Join(recoveryErrors...)
}

func priorityRecoveryPathKey(path string) string {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func (s *Service) beginPriorityOverride(ctx context.Context, cfg Config, runID string, auth Auth, overridePriority int) (*priorityOverrideLease, error) {
	if !cfg.TemporaryPriority || auth.Priority >= overridePriority {
		return nil, nil
	}
	if auth.RuntimeOnly || !strings.EqualFold(strings.TrimSpace(auth.Source), "file") {
		return nil, fmt.Errorf("target credential has no writable physical auth file")
	}
	for attempt := 0; attempt < 3; attempt++ {
		file, errGet := s.host.GetAuthFile(ctx, auth.AuthIndex)
		if errGet != nil {
			return nil, fmt.Errorf("read target credential file: %w", errGet)
		}
		if strings.TrimSpace(file.AuthIndex) != "" && strings.TrimSpace(file.AuthIndex) != strings.TrimSpace(auth.AuthIndex) {
			return nil, fmt.Errorf("target credential file auth_index mismatch")
		}
		if strings.TrimSpace(file.Name) == "" {
			return nil, fmt.Errorf("target credential file name is missing")
		}
		modified, marker, errBuild := buildPriorityOverrideJSON(file.JSON, auth.AuthIndex, runID, overridePriority)
		if errBuild != nil {
			return nil, errBuild
		}
		file.JSON = modified
		if errSave := s.host.SaveAuthFile(ctx, file); errSave != nil {
			if errors.Is(errSave, ErrAuthFileChanged) {
				continue
			}
			return nil, fmt.Errorf("save temporary credential priority: %w", errSave)
		}
		lease := &priorityOverrideLease{
			AuthIndex: auth.AuthIndex,
			AuthID:    auth.ID,
			Name:      file.Name,
			Marker:    marker,
		}
		if _, errWait := s.waitForRuntimePriority(ctx, cfg.PrioritySyncTimeout, auth.AuthIndex, auth.ID, overridePriority); errWait != nil {
			return lease, fmt.Errorf("confirm temporary credential priority: %w", errWait)
		}
		s.log("info", "credential priority temporarily aligned", map[string]any{
			"event":             "priority_override_applied",
			"run_id":            runID,
			"auth_index":        auth.AuthIndex,
			"original_priority": auth.Priority,
			"override_priority": overridePriority,
		})
		return lease, nil
	}
	return nil, ErrAuthFileChanged
}

func (s *Service) restorePriorityOverride(cfg Config, runID string, lease *priorityOverrideLease) error {
	if lease == nil {
		return nil
	}
	timeout := cfg.PrioritySyncTimeout
	if timeout <= 0 {
		timeout = defaultPrioritySyncTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	externalChange, _, errRestore := s.restorePriorityFile(ctx, timeout, lease.AuthIndex, lease.AuthID, &lease.Marker)
	if errRestore != nil {
		return errRestore
	}
	level := "info"
	if externalChange {
		level = "warn"
	}
	s.log(level, "credential priority restored", map[string]any{
		"event":           "priority_override_restored",
		"run_id":          runID,
		"auth_index":      lease.AuthIndex,
		"external_change": externalChange,
	})
	return nil
}

func (s *Service) restorePriorityFile(ctx context.Context, timeout time.Duration, authIndex, authID string, marker *priorityOverrideMarker) (bool, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		file, errGet := s.getAuthFileUntil(ctx, authIndex)
		if errGet != nil {
			return false, false, fmt.Errorf("read credential for priority restoration: %w", errGet)
		}
		restored, expectedPriority, needsSave, externalChange, errBuild := restorePriorityOverrideJSON(file.JSON, marker)
		if errBuild != nil {
			return false, false, errBuild
		}
		if !needsSave {
			if marker != nil {
				if _, errWait := s.waitForRuntimePriority(ctx, timeout, authIndex, authID, expectedPriority); errWait != nil {
					return false, false, fmt.Errorf("confirm externally restored credential priority: %w", errWait)
				}
			}
			return externalChange, false, nil
		}
		file.JSON = restored
		if errSave := s.host.SaveAuthFile(ctx, file); errSave != nil {
			if errors.Is(errSave, ErrAuthFileChanged) {
				continue
			}
			return false, false, fmt.Errorf("save credential priority restoration: %w", errSave)
		}
		if _, errWait := s.waitForRuntimePriority(ctx, timeout, authIndex, authID, expectedPriority); errWait != nil {
			return false, false, fmt.Errorf("confirm credential priority restoration: %w", errWait)
		}
		return externalChange, true, nil
	}
	return false, false, ErrAuthFileChanged
}

func (s *Service) getAuthFileUntil(ctx context.Context, authIndex string) (AuthFile, error) {
	var lastErr error
	for {
		file, errGet := s.host.GetAuthFile(ctx, authIndex)
		if errGet == nil {
			return file, nil
		}
		lastErr = errGet
		if !waitPriorityPoll(ctx) {
			if lastErr != nil {
				return AuthFile{}, lastErr
			}
			return AuthFile{}, ctx.Err()
		}
	}
}

func (s *Service) waitForRuntimePriority(ctx context.Context, timeout time.Duration, authIndex, authID string, expectedPriority int) (Auth, error) {
	if timeout <= 0 {
		timeout = defaultPrioritySyncTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		auth, errGet := s.host.GetRuntimeAuth(waitCtx, authIndex)
		if errGet == nil && strings.TrimSpace(auth.AuthIndex) == strings.TrimSpace(authIndex) &&
			(strings.TrimSpace(authID) == "" || strings.TrimSpace(auth.ID) == strings.TrimSpace(authID)) && auth.Priority == expectedPriority {
			return auth, nil
		}
		if !waitPriorityPoll(waitCtx) {
			return Auth{}, fmt.Errorf("CPA runtime did not publish priority %d before timeout", expectedPriority)
		}
	}
}

func waitPriorityPoll(ctx context.Context) bool {
	timer := time.NewTimer(priorityPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
