// Package pomerium renders Pomerium's configuration from the worker list.
//
// Pomerium is the control plane's only door for worker hosts. It
// requires a client certificate from the control plane's CA on every
// connection and rejects any other (downstream_mtls, enforcement
// reject_connection). Each flow is a route of its own, and each route
// allows only the certificates the worker list names for that flow. On
// every route Pomerium sets Sortie's worker identity header from the
// certificate's DNS name, replacing whatever the client sent.
package pomerium

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/workers"
)

// Settings are what the rendered configuration needs besides the
// worker list. The paths are inside Pomerium's container.
type Settings struct {
	// PublicURL is the https URL workers dial: the control plane's
	// host name and Pomerium's published port.
	PublicURL string
	// Address is where Pomerium listens in its container.
	Address string
	// Sortie is the base URL of Sortie on the internal network: its
	// address there, the only one it listens on.
	Sortie string
	// Egress is the base URL of the egress gate on the internal network.
	Egress string
	// IdentityHeader is Sortie's worker.identity_header.
	IdentityHeader string
	// CAFile is the control plane's CA certificate.
	CAFile string
	// CertFile and KeyFile are Pomerium's server certificate and key.
	CertFile, KeyFile string
}

// Defaults are the settings of the compose project.
var Defaults = Settings{
	Address:        ":8443",
	Sortie:         "http://10.89.71.10:7680",
	Egress:         "http://egress-gate:7691",
	IdentityHeader: "X-Sortie-Worker-Identity",
	CAFile:         "/pomerium/ca.crt",
	CertFile:       "/run/secrets/cp-pomerium-cert",
	KeyFile:        "/run/secrets/cp-pomerium-key",
}

// IdentityValue is what Pomerium sets the identity header to: the DNS
// subject alternative name of the client certificate, the worker's name.
const IdentityValue = "${pomerium.client_cert_san_dns}"

// Route is one Pomerium route.
type Route struct {
	Name                     string            `json:"name"`
	From                     string            `json:"from"`
	To                       string            `json:"to"`
	Prefix                   string            `json:"prefix,omitempty"`
	PrefixRewrite            string            `json:"prefix_rewrite,omitempty"`
	RegexRewritePattern      string            `json:"regex_rewrite_pattern,omitempty"`
	RegexRewriteSubstitution string            `json:"regex_rewrite_substitution,omitempty"`
	Timeout                  string            `json:"timeout,omitempty"`
	IdleTimeout              string            `json:"idle_timeout,omitempty"`
	AllowUpgrades            []string          `json:"allow_upgrades,omitempty"`
	PreserveHostHeader       bool              `json:"preserve_host_header,omitempty"`
	SetRequestHeaders        map[string]string `json:"set_request_headers"`
	PassIdentityHeaders      bool              `json:"pass_identity_headers"`
	Policy                   []map[string]any  `json:"policy"`
}

// Config is Pomerium's configuration file. JSON is YAML, so Pomerium
// reads it as config.yaml.
type Config struct {
	Address            string         `json:"address"`
	CertificateFile    string         `json:"certificate_file"`
	CertificateKeyFile string         `json:"certificate_key_file"`
	DownstreamMTLS     map[string]any `json:"downstream_mtls"`
	SkipXffAppend      bool           `json:"skip_xff_append"`
	Routes             []Route        `json:"routes"`
}

// StdioUpgrade is the Upgrade token of Sortie's stdio flow.
const StdioUpgrade = "sortie-stdio/1"

// Render builds Pomerium's configuration for the workers in l.
func Render(s Settings, l *workers.List) ([]byte, error) {
	cfg, err := Build(s, l)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	head := "# Rendered by cpctl render from the worker list; do not edit. JSON is YAML.\n"
	return append([]byte(head), append(b, '\n')...), nil
}

// Build builds Pomerium's configuration for the workers in l.
func Build(s Settings, l *workers.List) (*Config, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	pub, err := url.Parse(s.PublicURL)
	if err != nil || pub.Scheme != "https" || pub.Host == "" || pub.User != nil || (pub.Path != "" && pub.Path != "/") || pub.RawQuery != "" {
		return nil, fmt.Errorf("public URL %q: want https://host[:port] with nothing after the host", s.PublicURL)
	}
	from := "https://" + pub.Host
	for _, f := range []struct{ name, v string }{
		{"address", s.Address}, {"sortie", s.Sortie}, {"egress", s.Egress}, {"identity header", s.IdentityHeader},
		{"CA file", s.CAFile}, {"certificate file", s.CertFile}, {"key file", s.KeyFile},
	} {
		if f.v == "" {
			return nil, fmt.Errorf("the %s is missing", f.name)
		}
	}
	if strings.ContainsAny(s.IdentityHeader, " \t:") {
		return nil, fmt.Errorf("identity header %q is not a header name", s.IdentityHeader)
	}
	sortie := strings.TrimRight(s.Sortie, "/")
	headers := func() map[string]string { return map[string]string{s.IdentityHeader: IdentityValue} }
	route := func(flow string) Route {
		return Route{Name: flow, From: from, To: sortie, SetRequestHeaders: headers(), Policy: policy(l.Allowed(flow))}
	}

	control := route("control")
	control.Prefix = "/api/v1/workers/"

	stdio := route("stdio")
	stdio.Prefix = "/stdio/"
	stdio.PrefixRewrite = "/api/v1/flows/stdio/"
	stdio.AllowUpgrades = []string{StdioUpgrade}
	stdio.Timeout, stdio.IdleTimeout = "0s", "0s"

	// A worker sends a tool call for the guest's /<path> to
	// /mcp/jobs/{job}/attempts/{attempt}/<path>; Sortie serves the
	// attempt's tool server, as its /mcp, at the path without <path>.
	mcp := route("mcp")
	mcp.Prefix = "/mcp/jobs/"
	mcp.RegexRewritePattern = `^/mcp/jobs/([^/]+)/attempts/([0-9]+)(?:/.*)?$`
	mcp.RegexRewriteSubstitution = `/api/v1/flows/mcp/jobs/\1/attempts/\2`
	mcp.Timeout, mcp.IdleTimeout = "0s", "0s"

	bundles := route("bundles")
	bundles.Prefix = "/bundles/"
	bundles.PrefixRewrite = "/api/v1/flows/bundles/"
	bundles.Timeout = "0s"

	usage := route("usage")
	usage.Prefix = "/usage/"
	usage.PrefixRewrite = "/api/v1/flows/usage/"

	// The egress flow is an HTTP CONNECT proxy: every CONNECT that
	// reaches Pomerium, whatever host it names, goes on as a CONNECT to
	// the egress gate, which admits it by its job token and tunnels it
	// through nono.
	egress := route("egress")
	egress.From = "tcp+https://*"
	egress.PreserveHostHeader = true
	egress.To = strings.TrimRight(s.Egress, "/")

	cfg := &Config{
		Address:            s.Address,
		CertificateFile:    s.CertFile,
		CertificateKeyFile: s.KeyFile,
		DownstreamMTLS:     map[string]any{"ca_file": s.CAFile, "enforcement": "reject_connection"},
		SkipXffAppend:      true,
		Routes:             []Route{control, stdio, mcp, bundles, usage, egress},
	}
	return cfg, nil
}

// policy allows only the certificates with the given fingerprints, and
// denies everything when there are none.
func policy(fingerprints []string) []map[string]any {
	if len(fingerprints) == 0 {
		return []map[string]any{{"deny": map[string]any{"or": []any{map[string]any{"accept": true}}}}}
	}
	fps := make([]any, len(fingerprints))
	for i, f := range fingerprints {
		fps[i] = f
	}
	return []map[string]any{{"allow": map[string]any{"or": []any{
		map[string]any{"client_certificate": map[string]any{"fingerprint": fps}},
	}}}}
}
