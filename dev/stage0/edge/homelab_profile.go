package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"

	"gopkg.in/yaml.v3"
)

// HomelabProfileName is the meta.name of the egress profile of homelab jobs.
const HomelabProfileName = "ortus-stage0-egress-homelab"

// hostName is an exact, lower-case DNS name: no wildcard, as homelab's
// egress-hosts.yaml requires.
var hostName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// egressHosts is homelab's scripts/smolvm/egress-hosts.yaml: the hosts its
// jobs need, under hosts. The other sections (refused) say what they do
// not need, and allow nothing.
type egressHosts struct {
	Hosts []struct {
		Host string `yaml:"host"`
	} `yaml:"hosts"`
}

// readEgressHosts returns the hosts homelab's egress-hosts.yaml declares,
// and refuses a file that is missing, is not YAML, has no hosts list, or
// names a host that is not an exact DNS name or IP address.
func readEgressHosts(file string) ([]string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the operator names the file
	if err != nil {
		return nil, err
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	node, ok := doc["hosts"]
	if !ok || node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s: hosts is not a list", file)
	}
	var eh egressHosts
	if err := yaml.Unmarshal(b, &eh); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var hosts []string
	for i, e := range eh.Hosts {
		h := e.Host
		if !hostName.MatchString(h) && net.ParseIP(h) == nil {
			return nil, fmt.Errorf("%s: hosts[%d]: %q is not an exact host name or IP address", file, i, h)
		}
		if slices.Contains(hosts, h) {
			return nil, fmt.Errorf("%s: %s is listed twice", file, h)
		}
		hosts = append(hosts, h)
	}
	return hosts, nil
}

// renderHomelabProfile returns nono's profile for homelab jobs: exactly the
// hosts homelab's egress-hosts.yaml declares, plus the hosts of the
// credentials nono holds, which it takes, with those credentials, from the
// kit's egress profile (egress-profile.json). None of that profile's other
// hosts.
func renderHomelabProfile(baseFile, hostsFile string) ([]byte, error) {
	hosts, err := readEgressHosts(hostsFile)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(baseFile) // #nosec G304 -- the operator names the file
	if err != nil {
		return nil, err
	}
	var base struct {
		Network map[string]json.RawMessage `json:"network"`
	}
	if err := json.Unmarshal(b, &base); err != nil {
		return nil, fmt.Errorf("%s: %w", baseFile, err)
	}
	var creds map[string]struct {
		Upstream string `json:"upstream"`
	}
	if raw, ok := base.Network["custom_credentials"]; ok {
		if err := json.Unmarshal(raw, &creds); err != nil {
			return nil, fmt.Errorf("%s: network.custom_credentials: %w", baseFile, err)
		}
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("%s holds no credential", baseFile)
	}
	for name, c := range creds {
		u, err := url.Parse(c.Upstream)
		if err != nil || u.Hostname() == "" {
			return nil, fmt.Errorf("%s: credential %s has no upstream host", baseFile, name)
		}
		if !slices.Contains(hosts, u.Hostname()) {
			hosts = append(hosts, u.Hostname())
		}
	}
	slices.Sort(hosts)
	network := map[string]any{"allow_domain": hosts}
	for _, k := range []string{"credentials", "custom_credentials"} {
		if raw, ok := base.Network[k]; ok {
			network[k] = raw
		}
	}
	out, err := json.MarshalIndent(map[string]any{
		"meta": map[string]string{
			"name": HomelabProfileName,
			"description": "The egress of homelab jobs: the hosts in homelab's scripts/smolvm/egress-hosts.yaml at the commit " +
				"the kit runs, and those of the credentials of egress-profile.json. Rendered by stage0.sh; do not edit",
		},
		"network": network,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// homelabProfile is `stage0-edge homelab-profile`: it writes the profile
// of homelab jobs, or fails and leaves the file as it was.
func homelabProfile(args []string) error {
	fs := flag.NewFlagSet("homelab-profile", flag.ContinueOnError)
	base := fs.String("base", "", "the kit's egress profile, for its credentials")
	hosts := fs.String("hosts", "", "homelab's scripts/smolvm/egress-hosts.yaml")
	out := fs.String("out", "", "the file to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *base == "" || *hosts == "" || *out == "" || fs.NArg() > 0 {
		return errors.New("usage: stage0-edge homelab-profile -base egress-profile.json -hosts egress-hosts.yaml -out FILE")
	}
	b, err := renderHomelabProfile(*base, *hosts)
	if err != nil {
		return err
	}
	tmp := *out + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, *out); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
