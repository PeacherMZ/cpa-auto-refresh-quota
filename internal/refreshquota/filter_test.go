package refreshquota

import (
	"reflect"
	"testing"
)

func TestAuthEligibleAppliesSafetyAndConfiguredFilters(t *testing.T) {
	cfg := Config{
		Providers:          []string{"codex"},
		IncludeAuthIndices: []string{"auth-1", "auth-2"},
		ExcludeAuthIndices: []string{"auth-2"},
		PhysicalFilesOnly:  true,
		SkipUnavailable:    true,
	}
	base := Auth{ID: "id-1", AuthIndex: "auth-1", Provider: "codex", Source: "file"}

	tests := []struct {
		name       string
		auth       Auth
		wantOK     bool
		wantReason string
	}{
		{name: "eligible", auth: base, wantOK: true},
		{name: "missing id", auth: withAuth(base, func(a *Auth) { a.ID = "" }), wantReason: "missing_auth_id"},
		{name: "missing index", auth: withAuth(base, func(a *Auth) { a.AuthIndex = "" }), wantReason: "missing_auth_index"},
		{name: "disabled flag", auth: withAuth(base, func(a *Auth) { a.Disabled = true }), wantReason: "disabled"},
		{name: "disabled status", auth: withAuth(base, func(a *Auth) { a.Status = "DISABLED" }), wantReason: "disabled"},
		{name: "unavailable", auth: withAuth(base, func(a *Auth) { a.Unavailable = true }), wantReason: "unavailable"},
		{name: "runtime only", auth: withAuth(base, func(a *Auth) { a.RuntimeOnly = true }), wantReason: "not_physical_file"},
		{name: "physical runtime entry may report memory source", auth: withAuth(base, func(a *Auth) { a.Source = "memory" }), wantOK: true},
		{name: "provider", auth: withAuth(base, func(a *Auth) { a.Provider = "claude" }), wantReason: "provider_not_selected"},
		{name: "not included", auth: withAuth(base, func(a *Auth) { a.AuthIndex = "auth-3" }), wantReason: "auth_not_included"},
		{name: "excluded", auth: withAuth(base, func(a *Auth) { a.AuthIndex = "auth-2" }), wantReason: "auth_excluded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOK, gotReason := AuthEligible(cfg, tt.auth)
			if gotOK != tt.wantOK || gotReason != tt.wantReason {
				t.Fatalf("AuthEligible() = %v, %q; want %v, %q", gotOK, gotReason, tt.wantOK, tt.wantReason)
			}
		})
	}
}

func TestFilterAuthsAddsManualAllowlistAndSorts(t *testing.T) {
	cfg := Config{PhysicalFilesOnly: false, SkipUnavailable: false}
	auths := []Auth{
		{ID: "3", AuthIndex: "z", Provider: "codex", Source: "memory", RuntimeOnly: true},
		{ID: "2", AuthIndex: "b", Provider: "claude", Source: "file", Unavailable: true},
		{ID: "1", AuthIndex: "a", Provider: "claude", Source: "file"},
		{ID: "4", AuthIndex: "disabled", Provider: "claude", Source: "file", Disabled: true},
	}

	got := FilterAuths(cfg, auths, []string{"z", "a", "b"})
	want := []Auth{auths[2], auths[1], auths[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterAuths() = %#v, want %#v", got, want)
	}
}

func TestHasDuplicateAuthIdentity(t *testing.T) {
	tests := []struct {
		name  string
		auths []Auth
		want  bool
	}{
		{name: "unique", auths: []Auth{{ID: "id-a", AuthIndex: "auth-a"}, {ID: "id-b", AuthIndex: "auth-b"}}},
		{name: "duplicate id", auths: []Auth{{ID: "id-a", AuthIndex: "auth-a"}, {ID: " id-a ", AuthIndex: "auth-b"}}, want: true},
		{name: "duplicate index", auths: []Auth{{ID: "id-a", AuthIndex: "auth-a"}, {ID: "id-b", AuthIndex: " auth-a "}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDuplicateAuthIdentity(tt.auths); got != tt.want {
				t.Fatalf("hasDuplicateAuthIdentity() = %v, want %v", got, tt.want)
			}
		})
	}
}

func withAuth(auth Auth, change func(*Auth)) Auth {
	change(&auth)
	return auth
}
