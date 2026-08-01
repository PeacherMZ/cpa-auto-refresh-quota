package refreshquota

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxRememberedOccurrences   = 512
	priorityRecoveryTimeout    = 30 * time.Second
	priorityRecoveryEmptyGrace = 2 * time.Second
)

type Service struct {
	host Host
	now  func() time.Time

	configureMu sync.Mutex
	priorityMu  sync.Mutex
	mu          sync.Mutex
	wg          sync.WaitGroup

	config           Config
	configGeneration uint64
	configError      string
	next             *NextSchedule
	active           *RunReport
	last             *RunReport
	running          bool
	stopped          bool

	schedulerCancel         context.CancelFunc
	activeCancel            context.CancelFunc
	recoveryCancel          context.CancelFunc
	recoveryGeneration      uint64
	priorityRecoveryPending bool
	priorityRecoveryError   string

	occurrences           map[string]struct{}
	occurrenceOrder       []string
	skippedOccurrences    uint64
	lastSkippedOccurrence string
}

func NewService(host Host) *Service {
	return &Service{
		host:        host,
		now:         time.Now,
		occurrences: make(map[string]struct{}),
	}
}

// ApplyConfig atomically replaces the active configuration. Invalid configuration
// always cancels the previous schedule so a stale job cannot keep sending requests.
func (s *Service) ApplyConfig(raw []byte) error {
	s.configureMu.Lock()
	defer s.configureMu.Unlock()

	cfg, errParse := ParseConfig(raw)

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	if errParse == nil && s.configGeneration > 0 && s.configError == "" && s.config.Equivalent(cfg) {
		s.mu.Unlock()
		return nil
	}
	oldSchedulerCancel := s.schedulerCancel
	oldActiveCancel := s.activeCancel
	oldRecoveryCancel := s.recoveryCancel
	s.schedulerCancel = nil
	s.recoveryCancel = nil
	s.recoveryGeneration = 0
	s.priorityRecoveryPending = false
	s.priorityRecoveryError = ""
	s.configGeneration++
	generation := s.configGeneration
	s.next = nil
	if errParse != nil {
		s.config = Config{HostEnabled: true}
		s.configError = sanitizeError(errParse)
	} else {
		s.config = cfg.Clone()
		s.configError = ""
	}
	s.mu.Unlock()

	if oldSchedulerCancel != nil {
		oldSchedulerCancel()
	}
	if oldActiveCancel != nil {
		oldActiveCancel()
	}
	if oldRecoveryCancel != nil {
		oldRecoveryCancel()
	}

	if errParse != nil {
		return errParse
	}
	if cfg.HostEnabled && cfg.ScheduleEnabled {
		s.startScheduler(cfg.Clone(), generation)
	}
	if cfg.HostEnabled {
		s.startPriorityRecovery(cfg.Clone(), generation)
	}
	return nil
}

func (s *Service) startPriorityRecovery(cfg Config, generation uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), priorityRecoveryTimeout)
	s.mu.Lock()
	if s.stopped || generation != s.configGeneration {
		s.mu.Unlock()
		cancel()
		return
	}
	s.recoveryCancel = cancel
	s.recoveryGeneration = generation
	s.priorityRecoveryPending = true
	s.priorityRecoveryError = ""
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		defer func() {
			s.mu.Lock()
			if s.recoveryGeneration == generation {
				s.recoveryCancel = nil
				s.recoveryGeneration = 0
				s.priorityRecoveryPending = false
			}
			s.mu.Unlock()
		}()

		var stableSince time.Time
		stableFingerprint := ""
		for {
			if !s.priorityRecoveryStillCurrent(generation) {
				return
			}
			auths, errList := s.host.ListAuths(ctx)
			if errList == nil {
				fingerprint := authRecoveryFingerprint(auths)
				if stableSince.IsZero() || fingerprint != stableFingerprint {
					stableFingerprint = fingerprint
					stableSince = time.Now()
				}
			} else {
				stableSince = time.Time{}
				stableFingerprint = ""
			}
			ready := errList == nil && !stableSince.IsZero() && time.Since(stableSince) >= priorityRecoveryEmptyGrace
			if ready {
				if !s.lockPriority(ctx) {
					if errors.Is(ctx.Err(), context.DeadlineExceeded) {
						s.reportPriorityRecoveryTimeout(generation)
					}
					return
				}
				if !s.priorityRecoveryStillCurrent(generation) || ctx.Err() != nil {
					s.priorityMu.Unlock()
					return
				}
				changed, errRecover := s.recoverStalePriorityOverrides(ctx, cfg, auths)
				s.priorityMu.Unlock()
				if errRecover != nil {
					s.setPriorityRecoveryResult(generation, errRecover)
					s.log("error", "startup credential priority recovery failed", map[string]any{
						"event": "priority_recovery_failed",
						"error": sanitizeError(errRecover),
					})
					return
				}
				s.setPriorityRecoveryResult(generation, nil)
				if changed {
					s.log("info", "startup credential priority recovery completed", map[string]any{
						"event": "priority_recovery_completed",
					})
				}
				return
			}
			if !waitRecoveryRetry(ctx) {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					s.reportPriorityRecoveryTimeout(generation)
				}
				return
			}
		}
	}()
}

func (s *Service) lockPriority(ctx context.Context) bool {
	for {
		if s.priorityMu.TryLock() {
			return true
		}
		if !waitPriorityPoll(ctx) {
			return false
		}
	}
}

func (s *Service) reportPriorityRecoveryTimeout(generation uint64) {
	errTimeout := errors.New("timed out waiting to perform startup priority recovery")
	s.setPriorityRecoveryResult(generation, errTimeout)
	s.log("error", "startup credential priority recovery timed out", map[string]any{
		"event": "priority_recovery_failed",
		"error": sanitizeError(errTimeout),
	})
}

func (s *Service) priorityRecoveryStillCurrent(generation uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stopped && generation == s.configGeneration && s.recoveryGeneration == generation
}

func authRecoveryFingerprint(auths []Auth) string {
	items := make([]string, 0, len(auths))
	for _, auth := range auths {
		items = append(items, strings.Join([]string{
			strings.TrimSpace(auth.AuthIndex),
			strings.TrimSpace(auth.ID),
			strings.ToLower(strings.TrimSpace(auth.Source)),
			strings.ToLower(strings.TrimSpace(auth.Status)),
			fmt.Sprintf("%t:%t:%t:%d", auth.RuntimeOnly, auth.Disabled, auth.Unavailable, auth.Priority),
		}, "\x00"))
	}
	sort.Strings(items)
	return strings.Join(items, "\x01")
}

func (s *Service) setPriorityRecoveryResult(generation uint64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || generation != s.configGeneration || generation != s.recoveryGeneration {
		return
	}
	s.priorityRecoveryPending = false
	s.priorityRecoveryError = sanitizeError(err)
}

func waitRecoveryRetry(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) startScheduler(cfg Config, generation uint64) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.stopped || generation != s.configGeneration {
		s.mu.Unlock()
		cancel()
		return
	}
	s.schedulerCancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.scheduleLoop(ctx, cfg, generation)
	}()
}

func (s *Service) scheduleLoop(ctx context.Context, cfg Config, generation uint64) {
	for {
		now := s.now()
		next, label, ok := NextOccurrence(now, cfg.Location, cfg.Times)
		if !ok {
			s.clearNext(generation)
			return
		}
		occurrenceID := OccurrenceID(next, cfg.Location, label)
		s.publishNext(generation, next, occurrenceID, cfg.Location)

		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			s.clearNext(generation)
			return
		case <-timer.C:
		}

		if _, errStart := s.startRun(ctx, cfg, generation, "scheduled", occurrenceID, ManualRunOptions{}); errStart != nil {
			s.handleScheduleStartError(occurrenceID, errStart)
		}
	}
}

func (s *Service) publishNext(generation uint64, at time.Time, occurrenceID string, location *time.Location) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || generation != s.configGeneration {
		return
	}
	s.next = &NextSchedule{
		OccurrenceID: occurrenceID,
		Local:        at.In(location),
		UTC:          at.UTC(),
	}
}

func (s *Service) clearNext(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == s.configGeneration {
		s.next = nil
	}
}

func (s *Service) handleScheduleStartError(occurrenceID string, errStart error) {
	if errStart != ErrBusy && errStart != ErrDuplicate {
		s.log("error", "scheduled refresh was not started", map[string]any{
			"event":         "schedule_start_failed",
			"occurrence_id": occurrenceID,
			"error":         sanitizeError(errStart),
		})
		return
	}
	if errStart == ErrDuplicate {
		return
	}
	s.mu.Lock()
	s.skippedOccurrences++
	s.lastSkippedOccurrence = occurrenceID
	s.mu.Unlock()
	s.log("warn", "scheduled refresh skipped because another run is active", map[string]any{
		"event":         "schedule_overlap_skipped",
		"occurrence_id": occurrenceID,
	})
}

func (s *Service) StartManual(options ManualRunOptions) (string, error) {
	s.mu.Lock()
	cfg := s.config.Clone()
	generation := s.configGeneration
	s.mu.Unlock()
	return s.startRun(context.Background(), cfg, generation, "manual", "", options)
}

func (s *Service) startRun(parent context.Context, cfg Config, expectedGeneration uint64, trigger, occurrenceID string, options ManualRunOptions) (string, error) {
	if parent == nil {
		parent = context.Background()
	}
	if strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.Message) == "" {
		return "", ErrNotConfigured
	}

	runID, errID := randomID()
	if errID != nil {
		return "", fmt.Errorf("create run id: %w", errID)
	}

	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return "", ErrStopped
	}
	if expectedGeneration != s.configGeneration {
		s.mu.Unlock()
		return "", ErrStopped
	}
	if !cfg.HostEnabled {
		s.mu.Unlock()
		return "", ErrStopped
	}
	if occurrenceID != "" {
		if _, exists := s.occurrences[occurrenceID]; exists {
			s.mu.Unlock()
			return "", ErrDuplicate
		}
		s.rememberOccurrenceLocked(occurrenceID)
	}
	if s.running {
		s.mu.Unlock()
		return "", ErrBusy
	}
	runCtx, cancel := context.WithCancel(parent)
	report := &RunReport{
		RunID:        runID,
		Trigger:      trigger,
		OccurrenceID: occurrenceID,
		DryRun:       options.DryRun,
		StartedAt:    s.now().UTC(),
	}
	s.running = true
	s.active = cloneRunReport(report)
	s.activeCancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer cancel()
		s.executeRun(runCtx, cfg.Clone(), options, report)
	}()
	return runID, nil
}

func (s *Service) rememberOccurrenceLocked(occurrenceID string) {
	if occurrenceID == "" {
		return
	}
	s.occurrences[occurrenceID] = struct{}{}
	s.occurrenceOrder = append(s.occurrenceOrder, occurrenceID)
	for len(s.occurrenceOrder) > maxRememberedOccurrences {
		oldest := s.occurrenceOrder[0]
		s.occurrenceOrder = s.occurrenceOrder[1:]
		delete(s.occurrences, oldest)
	}
}

func (s *Service) executeRun(ctx context.Context, cfg Config, options ManualRunOptions, report *RunReport) {
	s.log("info", "refresh run started", map[string]any{
		"event":         "run_started",
		"run_id":        report.RunID,
		"trigger":       report.Trigger,
		"occurrence_id": report.OccurrenceID,
		"model":         cfg.Model,
		"dry_run":       report.DryRun,
	})
	s.priorityMu.Lock()
	defer s.priorityMu.Unlock()

	auths, errList := s.host.ListAuths(ctx)
	if errList != nil {
		report.Error = "host auth listing failed"
		if ctx.Err() != nil {
			report.Canceled = true
		}
		s.finishRun(report)
		return
	}
	recovered, errRecover := s.recoverStalePriorityOverrides(ctx, cfg, auths)
	if errRecover != nil {
		report.Error = "stale auth priority restoration failed"
		if ctx.Err() != nil {
			report.Canceled = true
		}
		s.finishRun(report)
		return
	}
	if recovered {
		auths, errList = s.host.ListAuths(ctx)
		if errList != nil {
			report.Error = "host auth listing failed after priority recovery"
			if ctx.Err() != nil {
				report.Canceled = true
			}
			s.finishRun(report)
			return
		}
	}
	targets := FilterAuths(cfg, auths, options.AuthIndices)
	highestPriority, hasHighestPriority := highestAvailablePriority(auths)
	report.TargetCount = len(targets)
	s.publishActive(report)
	if hasDuplicateAuthIdentity(targets) {
		report.Error = "host auth listing contains duplicate auth identities"
		s.finishRun(report)
		return
	}

	if options.DryRun {
		for _, auth := range targets {
			desiredPriority := desiredPriorityForAuth(auth, highestPriority, hasHighestPriority)
			result := AuthResult{
				AuthIndex:                auth.AuthIndex,
				Provider:                 auth.Provider,
				Priority:                 auth.Priority,
				PriorityOverrideRequired: cfg.TemporaryPriority && auth.Priority < desiredPriority,
				Outcome:                  "selected",
			}
			if result.PriorityOverrideRequired {
				result.PriorityOverrideTo = desiredPriority
			}
			report.Results = append(report.Results, result)
		}
		s.finishRun(report)
		return
	}

	body, errBody := BuildRequestBody(cfg)
	if errBody != nil {
		report.Error = sanitizeError(errBody)
		s.finishRun(report)
		return
	}

	for index, listedAuth := range targets {
		if errContext := ctx.Err(); errContext != nil {
			report.Canceled = true
			report.SkippedCount += len(targets) - index
			break
		}

		result := AuthResult{AuthIndex: listedAuth.AuthIndex, Provider: listedAuth.Provider, Priority: listedAuth.Priority}
		runtimeAuth, errRuntime := s.host.GetRuntimeAuth(ctx, listedAuth.AuthIndex)
		if errRuntime != nil {
			result.Outcome = "failed"
			result.Error = "runtime auth check failed"
			report.FailedCount++
			report.Results = append(report.Results, result)
			s.publishActive(report)
			s.logAuthResult(report, result, cfg.Model)
			if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
				report.Canceled = true
				report.SkippedCount += len(targets) - index - 1
				break
			}
			continue
		}
		if strings.TrimSpace(runtimeAuth.AuthIndex) != strings.TrimSpace(listedAuth.AuthIndex) {
			result.Provider = runtimeAuth.Provider
			result.Outcome = "failed"
			result.Error = "runtime auth index mismatch"
			report.FailedCount++
			report.Results = append(report.Results, result)
			s.publishActive(report)
			s.logAuthResult(report, result, cfg.Model)
			if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
				report.Canceled = true
				report.SkippedCount += len(targets) - index - 1
				break
			}
			continue
		}
		if strings.TrimSpace(runtimeAuth.ID) != strings.TrimSpace(listedAuth.ID) {
			result.Provider = runtimeAuth.Provider
			result.Outcome = "failed"
			result.Error = "runtime auth id mismatch"
			report.FailedCount++
			report.Results = append(report.Results, result)
			s.publishActive(report)
			s.logAuthResult(report, result, cfg.Model)
			if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
				report.Canceled = true
				report.SkippedCount += len(targets) - index - 1
				break
			}
			continue
		}
		if ok, reason := AuthEligible(cfg, runtimeAuth); !ok {
			result.Provider = runtimeAuth.Provider
			result.Outcome = "skipped"
			result.Error = reason
			report.SkippedCount++
			report.Results = append(report.Results, result)
			s.publishActive(report)
			s.logAuthResult(report, result, cfg.Model)
			if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
				report.Canceled = true
				report.SkippedCount += len(targets) - index - 1
				break
			}
			continue
		}

		result.AuthIndex = runtimeAuth.AuthIndex
		result.Provider = runtimeAuth.Provider
		result.Priority = runtimeAuth.Priority
		desiredPriority := desiredPriorityForAuth(runtimeAuth, highestPriority, hasHighestPriority)
		result.PriorityOverrideRequired = cfg.TemporaryPriority && runtimeAuth.Priority < desiredPriority
		if result.PriorityOverrideRequired {
			result.PriorityOverrideTo = desiredPriority
		}
		started := s.now()
		lease, errPriority := s.beginPriorityOverride(ctx, cfg, report.RunID, runtimeAuth, desiredPriority)
		if errPriority != nil {
			restoreErr := s.restorePriorityOverride(cfg, report.RunID, lease)
			result.DurationMS = s.now().Sub(started).Milliseconds()
			result.Outcome = "failed"
			result.Error = "temporary auth priority update failed"
			report.FailedCount++
			if restoreErr != nil {
				result.Error = "auth priority restore failed"
				report.Error = "auth priority restore failed; remaining credentials were not attempted"
			}
			report.Results = append(report.Results, result)
			s.publishActive(report)
			s.logAuthResult(report, result, cfg.Model)
			if restoreErr != nil {
				report.SkippedCount += len(targets) - index - 1
				break
			}
			if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
				report.Canceled = true
				report.SkippedCount += len(targets) - index - 1
				break
			}
			continue
		}
		executeCtx, executeCancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		response, errExecute := s.host.ExecutePinned(executeCtx, PinnedModelRequest{
			AuthID:        runtimeAuth.ID,
			EntryProtocol: cfg.EntryProtocol,
			ExitProtocol:  cfg.ExitProtocol,
			Model:         cfg.Model,
			Body:          append([]byte(nil), body...),
			Headers:       make(http.Header),
		})
		executeCancel()
		errRestore := s.restorePriorityOverride(cfg, report.RunID, lease)
		result.DurationMS = s.now().Sub(started).Milliseconds()
		if errRestore != nil {
			result.Outcome = "failed"
			result.Error = "auth priority restore failed"
			report.FailedCount++
			report.Error = "auth priority restore failed; remaining credentials were not attempted"
		} else if errExecute != nil {
			result.Outcome = "failed"
			result.Error = safeExecutionError(errExecute)
			report.FailedCount++
		} else {
			result.StatusCode = response.StatusCode
			result.ResponseBytes = len(response.Body)
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				result.Outcome = "succeeded"
				report.SuccessCount++
			} else {
				result.Outcome = "failed"
				result.Error = fmt.Sprintf("model callback returned HTTP %d", response.StatusCode)
				report.FailedCount++
			}
		}
		report.Results = append(report.Results, result)
		s.publishActive(report)
		s.logAuthResult(report, result, cfg.Model)
		if errRestore != nil {
			report.SkippedCount += len(targets) - index - 1
			break
		}
		if ctx.Err() != nil {
			report.Canceled = true
			report.SkippedCount += len(targets) - index - 1
			break
		}

		if !s.waitBetweenAuths(ctx, cfg.DelayBetweenAuths, index, len(targets)) {
			report.Canceled = true
			report.SkippedCount += len(targets) - index - 1
			break
		}
	}
	s.finishRun(report)
}

func (s *Service) waitBetweenAuths(ctx context.Context, delay time.Duration, index, total int) bool {
	if ctx.Err() != nil {
		return false
	}
	if delay <= 0 || index >= total-1 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) logAuthResult(report *RunReport, result AuthResult, model string) {
	level := "info"
	if result.Outcome == "failed" {
		level = "error"
	} else if result.Outcome == "skipped" {
		level = "warn"
	}
	fields := map[string]any{
		"event":       "auth_completed",
		"run_id":      report.RunID,
		"auth_index":  result.AuthIndex,
		"provider":    result.Provider,
		"model":       model,
		"outcome":     result.Outcome,
		"status_code": result.StatusCode,
		"duration_ms": result.DurationMS,
	}
	if result.Error != "" {
		fields["error"] = result.Error
	}
	s.log(level, "credential refresh request completed", fields)
}

func (s *Service) publishActive(report *RunReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.RunID == report.RunID {
		s.active = cloneRunReport(report)
	}
}

func (s *Service) finishRun(report *RunReport) {
	finished := s.now().UTC()
	report.FinishedAt = &finished
	report.DurationMS = finished.Sub(report.StartedAt).Milliseconds()

	s.mu.Lock()
	if s.active != nil && s.active.RunID == report.RunID {
		s.active = nil
		s.running = false
		s.activeCancel = nil
		s.last = cloneRunReport(report)
	}
	s.mu.Unlock()

	s.log("info", "refresh run finished", map[string]any{
		"event":         "run_finished",
		"run_id":        report.RunID,
		"target_count":  report.TargetCount,
		"success_count": report.SuccessCount,
		"failed_count":  report.FailedCount,
		"skipped_count": report.SkippedCount,
		"canceled":      report.Canceled,
		"duration_ms":   report.DurationMS,
		"error":         report.Error,
	})
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := Status{
		ConfigGeneration:        s.configGeneration,
		Config:                  s.config.Summary(),
		ConfigError:             s.configError,
		PriorityRecoveryPending: s.priorityRecoveryPending,
		PriorityRecoveryError:   s.priorityRecoveryError,
		Running:                 s.running,
		ActiveRun:               cloneRunReport(s.active),
		LastRun:                 cloneRunReport(s.last),
		SkippedOccurrences:      s.skippedOccurrences,
		LastSkippedOccurrence:   s.lastSkippedOccurrence,
		Stopped:                 s.stopped,
	}
	if s.next != nil {
		next := *s.next
		status.Next = &next
	}
	return status
}

func (s *Service) Shutdown() {
	s.configureMu.Lock()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		s.configureMu.Unlock()
		return
	}
	s.stopped = true
	s.next = nil
	schedulerCancel := s.schedulerCancel
	activeCancel := s.activeCancel
	recoveryCancel := s.recoveryCancel
	s.schedulerCancel = nil
	s.recoveryCancel = nil
	s.recoveryGeneration = 0
	s.priorityRecoveryPending = false
	s.mu.Unlock()
	s.configureMu.Unlock()

	if schedulerCancel != nil {
		schedulerCancel()
	}
	if activeCancel != nil {
		activeCancel()
	}
	if recoveryCancel != nil {
		recoveryCancel()
	}
	s.wg.Wait()
}

func (s *Service) log(level, message string, fields map[string]any) {
	if s.host == nil {
		return
	}
	_ = s.host.Log(context.Background(), level, message, fields)
}

func randomID() (string, error) {
	var raw [12]byte
	if _, errRead := rand.Read(raw[:]); errRead != nil {
		return "", errRead
	}
	return hex.EncodeToString(raw[:]), nil
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' && r != '\n' {
			return -1
		}
		return r
	}, err.Error())
	text = strings.TrimSpace(text)
	const max = 512
	if len(text) > max {
		text = text[:max]
	}
	return text
}

func safeExecutionError(err error) string {
	if err == nil {
		return ""
	}
	safe := sanitizeError(err)
	lower := strings.ToLower(safe)
	switch {
	case strings.Contains(lower, "cpa scheduler did not select the requested auth"):
		return "CPA scheduler did not select target auth"
	case strings.Contains(lower, "target_auth_unavailable"), strings.Contains(lower, "requested auth is not available to the cpa scheduler"):
		return "target auth is unavailable to CPA scheduler"
	case strings.Contains(lower, "invalid_routing_token"), strings.Contains(lower, "invalid cpa auto-refresh routing token header"):
		return "CPA routing token was rejected"
	case strings.Contains(lower, "host auth manager is unavailable"):
		return "host auth manager is unavailable"
	case strings.Contains(lower, "host model executor is unavailable"):
		return "host model executor is unavailable"
	case strings.Contains(lower, "context canceled"):
		return "context canceled"
	case strings.Contains(lower, "context deadline exceeded"):
		return "context deadline exceeded"
	}
	return "host model execution failed"
}
