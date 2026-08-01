package refreshquota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	priorityOverrideMarkerKey = "_cpa_auto_refresh_quota_priority_override"
	priorityOverrideOwner     = "cpa-auto-refresh-quota"
	priorityOverrideVersion   = 1
	maxAuthFileBytes          = 4 * 1024 * 1024
)

type priorityOverrideMarker struct {
	Version                 int             `json:"version"`
	Owner                   string          `json:"owner"`
	RunID                   string          `json:"run_id"`
	AuthIndex               string          `json:"auth_index"`
	OverridePriority        int             `json:"override_priority"`
	OriginalPriorityPresent bool            `json:"original_priority_present"`
	OriginalPriority        json.RawMessage `json:"original_priority,omitempty"`
	OriginalFilePriority    int             `json:"original_file_priority"`
}

type priorityOverrideLease struct {
	AuthIndex string
	AuthID    string
	Name      string
	Marker    priorityOverrideMarker
}

func highestAvailablePriority(auths []Auth) (int, bool) {
	highest := 0
	found := false
	for _, auth := range auths {
		if auth.Disabled || strings.EqualFold(strings.TrimSpace(auth.Status), "disabled") {
			continue
		}
		if !found || auth.Priority > highest {
			highest = auth.Priority
			found = true
		}
	}
	return highest, found
}

func desiredPriorityForAuth(auth Auth, highest int, found bool) int {
	if !found || highest < auth.Priority {
		return auth.Priority
	}
	return highest
}

func buildPriorityOverrideJSON(raw json.RawMessage, authIndex, runID string, overridePriority int) (json.RawMessage, priorityOverrideMarker, error) {
	fields, errDecode := decodeAuthFileObject(raw)
	if errDecode != nil {
		return nil, priorityOverrideMarker{}, errDecode
	}
	if _, exists := fields[priorityOverrideMarkerKey]; exists {
		return nil, priorityOverrideMarker{}, fmt.Errorf("credential already contains a priority override marker")
	}

	originalPriority, originalPresent := fields["priority"]
	marker := priorityOverrideMarker{
		Version:                 priorityOverrideVersion,
		Owner:                   priorityOverrideOwner,
		RunID:                   strings.TrimSpace(runID),
		AuthIndex:               strings.TrimSpace(authIndex),
		OverridePriority:        overridePriority,
		OriginalPriorityPresent: originalPresent,
		OriginalFilePriority:    priorityFromRawJSON(originalPriority),
	}
	if originalPresent {
		marker.OriginalPriority = append(json.RawMessage(nil), originalPriority...)
	}
	markerJSON, errMarker := json.Marshal(marker)
	if errMarker != nil {
		return nil, priorityOverrideMarker{}, fmt.Errorf("encode priority override marker: %w", errMarker)
	}
	fields["priority"] = json.RawMessage(strconv.Itoa(overridePriority))
	fields[priorityOverrideMarkerKey] = markerJSON
	modified, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		return nil, priorityOverrideMarker{}, fmt.Errorf("encode credential priority override: %w", errMarshal)
	}
	return modified, marker, nil
}

func priorityOverrideMarkerFromJSON(raw json.RawMessage) (priorityOverrideMarker, bool, error) {
	fields, errDecode := decodeAuthFileObject(raw)
	if errDecode != nil {
		return priorityOverrideMarker{}, false, errDecode
	}
	rawMarker, exists := fields[priorityOverrideMarkerKey]
	if !exists {
		return priorityOverrideMarker{}, false, nil
	}
	marker, errMarker := decodePriorityOverrideMarker(rawMarker)
	if errMarker != nil {
		return priorityOverrideMarker{}, true, errMarker
	}
	return marker, true, nil
}

func restorePriorityOverrideJSON(raw json.RawMessage, fallback *priorityOverrideMarker) (json.RawMessage, int, bool, bool, error) {
	fields, errDecode := decodeAuthFileObject(raw)
	if errDecode != nil {
		return nil, 0, false, false, errDecode
	}

	marker := priorityOverrideMarker{}
	rawMarker, markerPresent := fields[priorityOverrideMarkerKey]
	if markerPresent {
		var errMarker error
		marker, errMarker = decodePriorityOverrideMarker(rawMarker)
		if errMarker != nil {
			return nil, 0, false, false, errMarker
		}
		if fallback != nil && (marker.AuthIndex != fallback.AuthIndex || marker.RunID != fallback.RunID || marker.OverridePriority != fallback.OverridePriority) {
			return nil, 0, false, false, fmt.Errorf("credential priority override marker belongs to another transaction")
		}
	} else if fallback != nil {
		marker = *fallback
	} else {
		return append(json.RawMessage(nil), raw...), 0, false, false, nil
	}

	currentPriority := priorityFromRawJSON(fields["priority"])
	externalChange := currentPriority != marker.OverridePriority
	expectedPriority := currentPriority
	if !externalChange {
		expectedPriority = marker.OriginalFilePriority
		if marker.OriginalPriorityPresent {
			fields["priority"] = append(json.RawMessage(nil), marker.OriginalPriority...)
		} else {
			delete(fields, "priority")
		}
	}
	delete(fields, priorityOverrideMarkerKey)

	if !markerPresent && externalChange {
		return append(json.RawMessage(nil), raw...), expectedPriority, false, true, nil
	}
	restored, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		return nil, 0, false, false, fmt.Errorf("encode restored credential priority: %w", errMarshal)
	}
	return restored, expectedPriority, true, externalChange, nil
}

func decodePriorityOverrideMarker(raw json.RawMessage) (priorityOverrideMarker, error) {
	var marker priorityOverrideMarker
	if errDecode := json.Unmarshal(raw, &marker); errDecode != nil {
		return priorityOverrideMarker{}, fmt.Errorf("decode priority override marker: %w", errDecode)
	}
	if marker.Version != priorityOverrideVersion || marker.Owner != priorityOverrideOwner || strings.TrimSpace(marker.AuthIndex) == "" {
		return priorityOverrideMarker{}, fmt.Errorf("credential contains an unsupported priority override marker")
	}
	if marker.OriginalPriorityPresent && len(bytes.TrimSpace(marker.OriginalPriority)) == 0 {
		return priorityOverrideMarker{}, fmt.Errorf("priority override marker is missing the original priority")
	}
	return marker, nil
}

func decodeAuthFileObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("credential JSON is empty")
	}
	if len(trimmed) > maxAuthFileBytes {
		return nil, fmt.Errorf("credential JSON exceeds %d bytes", maxAuthFileBytes)
	}
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(trimmed, &fields); errDecode != nil {
		return nil, fmt.Errorf("decode credential JSON: %w", errDecode)
	}
	if fields == nil {
		return nil, fmt.Errorf("credential JSON must be an object")
	}
	return fields, nil
}

func priorityFromRawJSON(raw json.RawMessage) int {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return 0
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return 0
	}
	switch typed := value.(type) {
	case json.Number:
		if integer, errInteger := typed.Int64(); errInteger == nil {
			return int(integer)
		}
		if floating, errFloat := typed.Float64(); errFloat == nil {
			return int(floating)
		}
	case string:
		if parsed, errParse := strconv.Atoi(strings.TrimSpace(typed)); errParse == nil {
			return parsed
		}
	}
	return 0
}
