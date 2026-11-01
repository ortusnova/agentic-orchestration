package otlp

import (
	"net/http"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/secrets"
)

// RegisterSecrets tells the masking which values must never leave as
// telemetry: the supervisor's session token, the values of secret-named
// variables in environ, and the values of the export headers.
func RegisterSecrets(sessionToken string, environ []string, headers http.Header) {
	if sessionToken != "" {
		secrets.Add("session token", sessionToken)
	}
	secrets.AddEnviron(environ)
	for name, values := range headers {
		for _, v := range values {
			secrets.Add("telemetry header "+name, v)
		}
	}
}
