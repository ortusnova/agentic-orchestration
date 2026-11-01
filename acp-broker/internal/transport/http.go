package transport

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// Defaults for the HTTP transport.
const (
	DefaultHeartbeat    = 15 * time.Second
	DefaultWriteTimeout = 10 * time.Second
	maxCommandBytes     = 1 << 20
)

// ErrNoViewerToken is returned when an HTTP listener is configured
// without a viewer token.
var ErrNoViewerToken = errors.New("an HTTP listener needs a viewer token")

// HTTPConfig configures the HTTP transport.
type HTTPConfig struct {
	// Token is the viewer token every request must present. Required.
	Token string

	// PublicURL is the address people use to reach this listener. An
	// https URL makes the session cookie Secure.
	PublicURL string

	// CookieTTL is the lifetime of a browser session cookie. Zero
	// selects DefaultCookieTTL.
	CookieTTL time.Duration

	// Heartbeat is the interval of the keepalive comment on an event
	// stream. Zero selects DefaultHeartbeat.
	Heartbeat time.Duration

	// WriteTimeout bounds each write to a client; a client that does not
	// take its events within it is disconnected. Zero selects
	// DefaultWriteTimeout.
	WriteTimeout time.Duration

	// SourceURL is where the people who use this broker can get its
	// source, as the AGPL requires. Empty selects DefaultSourceURL.
	SourceURL string

	Logger *slog.Logger
}

// DefaultSourceURL is where the source of this broker is published.
// Whoever runs a modified broker points SourceURL at theirs.
const DefaultSourceURL = "https://github.com/ortusnova/agentic-orchestration"

// About is the answer to GET /source.
type About struct {
	Name      string `json:"name"`
	License   string `json:"license"`
	SourceURL string `json:"source_url"`
}

// HTTP serves the viewer protocol over HTTP: Server-Sent Events on
// GET /events, one command per POST /commands, and GET /state. GET /source
// says, without a token, where to get the broker's source.
type HTTP struct {
	b        *broker.Broker
	cfg      HTTPConfig
	log      *slog.Logger
	sessions *cookieSessions
	mux      *http.ServeMux
}

// NewHTTP returns the HTTP transport for b. It refuses a configuration
// without a viewer token.
func NewHTTP(b *broker.Broker, cfg HTTPConfig) (*HTTP, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrNoViewerToken
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = DefaultHeartbeat
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.CookieTTL <= 0 {
		cfg.CookieTTL = DefaultCookieTTL
	}
	if cfg.SourceURL == "" {
		cfg.SourceURL = DefaultSourceURL
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	h := &HTTP{b: b, cfg: cfg, log: log, sessions: newCookieSessions(), mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /events", h.authed(h.events))
	h.mux.HandleFunc("POST /commands", h.authed(h.commands))
	h.mux.HandleFunc("GET /state", h.authed(h.state))
	h.mux.HandleFunc("POST /login", h.login)
	h.mux.HandleFunc("POST /logout", h.logout)
	h.mux.HandleFunc("GET /source", h.source)
	return h, nil
}

func (h *HTTP) source(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, About{Name: "acp-broker", License: "AGPL-3.0-or-later", SourceURL: h.cfg.SourceURL})
}

// Handler returns the transport's request handler.
func (h *HTTP) Handler() http.Handler { return h.mux }

// tokenMatches compares a presented credential with the viewer token in
// constant time.
func (h *HTTP) tokenMatches(presented string) bool {
	return presented != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(h.cfg.Token)) == 1
}

func bearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// authed admits a request carrying the viewer token as a bearer header
// or a valid session cookie. A token anywhere else, the URL included, is
// not looked at.
func (h *HTTP) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.tokenMatches(bearer(r)) || h.sessions.valid(r) {
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, broker.Event{Type: "error", Message: "unauthorized"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *HTTP) state(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.b.State())
}

func (h *HTTP) commands(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxCommandBytes))
	var cmd broker.Command
	if err == nil {
		err = json.Unmarshal(data, &cmd)
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, broker.Event{Type: "error", Message: "bad command: " + err.Error()})
		return
	}
	reply := h.b.Do(cmd)
	status := http.StatusOK
	if reply.Type == "error" {
		status = http.StatusConflict
	}
	writeJSON(w, status, reply)
}

// events streams the session as Server-Sent Events. A logged event
// carries its sequence number as the event ID, so a client that
// reconnects with Last-Event-ID resumes after it.
func (h *HTTP) events(w http.ResponseWriter, r *http.Request) {
	var v *broker.Viewer
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		after, err := strconv.ParseInt(last, 10, 64)
		if err != nil || after < 0 {
			writeJSON(w, http.StatusBadRequest, broker.Event{Type: "error", Message: "Last-Event-ID must be an event sequence number"})
			return
		}
		v = h.b.Resume(after)
	} else {
		v = h.b.Attach()
	}
	defer v.Close()

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(chunk []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(h.cfg.WriteTimeout))
		if _, err := w.Write(chunk); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	if !write([]byte(": connected\n\n")) {
		return
	}

	heartbeat := time.NewTicker(h.cfg.Heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if !write([]byte(": heartbeat\n\n")) {
				return
			}
		case rec, ok := <-v.Lines():
			if !ok {
				return
			}
			if !write(sseFrame(rec)) {
				h.log.Info("dropping an HTTP viewer that stopped reading")
				return
			}
		}
	}
}

// sseFrame renders one record as a Server-Sent Events frame.
func sseFrame(rec broker.Record) []byte {
	var b strings.Builder
	if rec.Seq > 0 {
		b.WriteString("id: ")
		b.WriteString(strconv.FormatInt(rec.Seq, 10))
		b.WriteByte('\n')
	}
	b.WriteString("data: ")
	b.Write(bytesTrimNewline(rec.Data))
	b.WriteString("\n\n")
	return []byte(b.String())
}

func bytesTrimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
