package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
)

// liveProofHosts is egress-live-proof.json: what the live-proof profile
// allows on top of the kit's egress profile.
type liveProofHosts struct {
	Meta struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"meta"`
	AllowDomain []string `json:"allow_domain"`
}

// renderProfile returns the live-proof profile: the kit's egress profile
// (egress-profile.json), with its credentials, and every host of extra
// added to the hosts it allows.
func renderProfile(baseFile, extraFile string) ([]byte, error) {
	b, err := os.ReadFile(baseFile) // #nosec G304 G703 -- the operator names the file
	if err != nil {
		return nil, err
	}
	var base map[string]any
	if err := json.Unmarshal(b, &base); err != nil {
		return nil, fmt.Errorf("%s: %w", baseFile, err)
	}
	e, err := os.ReadFile(extraFile) // #nosec G304 G703 -- the operator names the file
	if err != nil {
		return nil, err
	}
	var extra liveProofHosts
	if err := json.Unmarshal(e, &extra); err != nil {
		return nil, fmt.Errorf("%s: %w", extraFile, err)
	}
	if len(extra.AllowDomain) == 0 {
		return nil, fmt.Errorf("%s allows no host", extraFile)
	}
	network, ok := base["network"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s has no network", baseFile)
	}
	raw, _ := network["allow_domain"].([]any)
	var hosts []string
	for _, h := range raw {
		s, ok := h.(string)
		if !ok {
			return nil, errors.New("network.allow_domain holds a value that is not a host")
		}
		hosts = append(hosts, s)
	}
	for _, h := range extra.AllowDomain {
		if !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	slices.Sort(hosts)
	network["allow_domain"] = hosts
	base["meta"] = map[string]any{"name": extra.Meta.Name, "description": extra.Meta.Description}
	out, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
