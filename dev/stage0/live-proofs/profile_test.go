package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type profile struct {
	Meta struct {
		Name string `json:"name"`
	} `json:"meta"`
	Network struct {
		AllowDomain       []string                   `json:"allow_domain"`
		Credentials       []string                   `json:"credentials"`
		CustomCredentials map[string]json.RawMessage `json:"custom_credentials"`
	} `json:"network"`
}

func readJSON(t *testing.T, file string, v any) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
}

// The live-proof profile is the kit's egress profile, its credentials
// included, with the hosts a live chain needs: Let's Encrypt staging for
// homelab's vm chain, which the kit's own profile refuses.
func TestTheLiveProofProfile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "live.json")
	if err := run([]string{"profile", "-base", "../egress-profile.json", "-extra", "../egress-live-proof.json", "-out", out}); err != nil {
		t.Fatal(err)
	}
	var base, live profile
	var extra liveProofHosts
	readJSON(t, "../egress-profile.json", &base)
	readJSON(t, "../egress-live-proof.json", &extra)
	readJSON(t, out, &live)
	if live.Meta.Name != "ortus-stage0-egress-live-proof" {
		t.Errorf("meta.name = %q", live.Meta.Name)
	}
	if !slices.Contains(extra.AllowDomain, "acme-staging-v02.api.letsencrypt.org") {
		t.Error("the live-proof profile does not allow Let's Encrypt staging")
	}
	for _, h := range extra.AllowDomain {
		if slices.Contains(base.Network.AllowDomain, h) {
			t.Errorf("the kit's profile allows %s already: every job reaches it, so it needs no live proof", h)
		}
	}
	for _, h := range append(slices.Clone(base.Network.AllowDomain), extra.AllowDomain...) {
		if !slices.Contains(live.Network.AllowDomain, h) {
			t.Errorf("the live-proof profile does not allow %s", h)
		}
	}
	if !slices.Equal(live.Network.Credentials, base.Network.Credentials) || len(live.Network.CustomCredentials) != len(base.Network.CustomCredentials) {
		t.Errorf("credentials = %v, want the kit's %v", live.Network.Credentials, base.Network.Credentials)
	}
}

func TestAProfileWithNoHostsIsRefused(t *testing.T) {
	extra := filepath.Join(t.TempDir(), "extra.json")
	if err := os.WriteFile(extra, []byte(`{"allow_domain": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := renderProfile("../egress-profile.json", extra); err == nil {
		t.Error("a live-proof profile that adds no host was rendered")
	}
}
