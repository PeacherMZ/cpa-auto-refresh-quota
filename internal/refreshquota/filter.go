package refreshquota

import (
	"sort"
	"strings"
)

func FilterAuths(cfg Config, auths []Auth, requestedAuthIndices []string) []Auth {
	requested := stringSet(requestedAuthIndices, false)
	out := make([]Auth, 0, len(auths))
	for _, auth := range auths {
		if len(requested) > 0 {
			if _, ok := requested[strings.TrimSpace(auth.AuthIndex)]; !ok {
				continue
			}
		}
		if ok, _ := AuthEligible(cfg, auth); ok {
			out = append(out, auth)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		leftProvider := strings.ToLower(strings.TrimSpace(out[i].Provider))
		rightProvider := strings.ToLower(strings.TrimSpace(out[j].Provider))
		if leftProvider != rightProvider {
			return leftProvider < rightProvider
		}
		return out[i].AuthIndex < out[j].AuthIndex
	})
	return out
}

func hasDuplicateAuthIdentity(auths []Auth) bool {
	seenIDs := make(map[string]struct{}, len(auths))
	seenIndices := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		authID := strings.TrimSpace(auth.ID)
		authIndex := strings.TrimSpace(auth.AuthIndex)
		if _, exists := seenIDs[authID]; exists {
			return true
		}
		if _, exists := seenIndices[authIndex]; exists {
			return true
		}
		seenIDs[authID] = struct{}{}
		seenIndices[authIndex] = struct{}{}
	}
	return false
}

func AuthEligible(cfg Config, auth Auth) (bool, string) {
	authID := strings.TrimSpace(auth.ID)
	authIndex := strings.TrimSpace(auth.AuthIndex)
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	status := strings.ToLower(strings.TrimSpace(auth.Status))

	if authID == "" {
		return false, "missing_auth_id"
	}
	if authIndex == "" {
		return false, "missing_auth_index"
	}
	if auth.Disabled || status == "disabled" {
		return false, "disabled"
	}
	if cfg.SkipUnavailable && auth.Unavailable {
		return false, "unavailable"
	}
	if cfg.PhysicalFilesOnly {
		if auth.RuntimeOnly {
			return false, "not_physical_file"
		}
	}
	if providers := stringSet(cfg.Providers, true); len(providers) > 0 {
		if _, ok := providers[provider]; !ok {
			return false, "provider_not_selected"
		}
	}
	if included := stringSet(cfg.IncludeAuthIndices, false); len(included) > 0 {
		if _, ok := included[authIndex]; !ok {
			return false, "auth_not_included"
		}
	}
	if excluded := stringSet(cfg.ExcludeAuthIndices, false); len(excluded) > 0 {
		if _, ok := excluded[authIndex]; ok {
			return false, "auth_excluded"
		}
	}
	return true, ""
}

func stringSet(values []string, lower bool) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, item := range values {
		item = strings.TrimSpace(item)
		if lower {
			item = strings.ToLower(item)
		}
		if item != "" {
			out[item] = struct{}{}
		}
	}
	return out
}
