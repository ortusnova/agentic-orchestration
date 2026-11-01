package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"runtime/debug"
	"strings"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/sortieclient"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry/otlp"
)

// telemetryConfig is the validated telemetry configuration, nil when no
// endpoint is set.
type telemetryConfig struct {
	endpoint   string
	headers    http.Header
	content    bool
	contentMax int
}

// telemetryFromFlags validates the telemetry flags. It reads the headers
// file at once, so a bad configuration stops the broker before the agent
// starts.
func telemetryFromFlags(f brokerFlags) (*telemetryConfig, error) {
	if f.otlpEndpoint == "" {
		if f.otlpHeadersFile != "" {
			return nil, errors.New("-otlp-headers-file needs -otlp-endpoint")
		}
		return nil, nil
	}
	u, err := url.Parse(f.otlpEndpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("-otlp-endpoint %q: want an http or https URL", f.otlpEndpoint)
	}
	if f.telemetryContentMax <= 0 {
		return nil, fmt.Errorf("-telemetry-content-max %d: want a positive number of bytes", f.telemetryContentMax)
	}
	cfg := &telemetryConfig{endpoint: f.otlpEndpoint, headers: http.Header{}, content: f.telemetryContent, contentMax: f.telemetryContentMax}
	if f.otlpHeadersFile != "" {
		if cfg.headers, err = readHeaders(f.otlpHeadersFile); err != nil {
			return nil, fmt.Errorf("-otlp-headers-file: %w", err)
		}
	}
	return cfg, nil
}

// readHeaders reads one "Name: value" header per line. Blank lines and
// lines starting with "#" are skipped.
func readHeaders(path string) (http.Header, error) {
	file, err := os.Open(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck // read only
	headers := http.Header{}
	scanner := bufio.NewScanner(file)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("line %d: want \"Name: value\"", n)
		}
		headers.Add(textproto.CanonicalMIMEHeaderKey(name), value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return headers, nil
}

// telemetryPipeline is a running recorder and exporter.
type telemetryPipeline struct {
	recorder *telemetry.Recorder
	exporter *otlp.Exporter
	stop     context.CancelFunc
	stopped  chan struct{}
}

// startTelemetry registers the secrets the export must never carry and
// starts the exporter. The recorder it returns feeds that exporter.
func startTelemetry(cfg *telemetryConfig, session sortieclient.Session, logger *slog.Logger) *telemetryPipeline {
	otlp.RegisterSecrets(session.Token, os.Environ(), cfg.headers)
	exporter := otlp.New(otlp.Config{
		Endpoint: cfg.endpoint,
		Headers:  cfg.headers,
		Encoder:  otlp.Encoder{ServiceVersion: serviceVersion(), ContentMax: cfg.contentMax},
		Logger:   logger,
	})
	ctx, stop := context.WithCancel(context.Background())
	p := &telemetryPipeline{exporter: exporter, stop: stop, stopped: make(chan struct{})}
	go func() {
		defer close(p.stopped)
		exporter.Run(ctx)
	}()
	p.recorder = telemetry.NewRecorder(telemetry.Options{
		Labels: telemetry.Labels{
			Issue: session.Identifier, Rule: session.Rule, ChainID: session.ChainID,
			DispatchID: session.DispatchID, Attempt: session.Attempt,
		},
		Content:    cfg.content,
		ContentMax: cfg.contentMax,
		Emit:       exporter.Enqueue,
	})
	logger.Info("exporting telemetry", slog.String("endpoint", cfg.endpoint), slog.Bool("content", cfg.content))
	return p
}

// finish stops the exporter and makes one last attempt, bounded, to send
// what it still holds.
func (p *telemetryPipeline) finish() {
	p.stop()
	<-p.stopped
	ctx, cancel := context.WithTimeout(context.Background(), otlp.FinalFlushBound)
	defer cancel()
	p.exporter.Flush(ctx)
}

// serviceVersion names the build in exported telemetry.
func serviceVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return s.Value[:12]
		}
	}
	return ""
}
