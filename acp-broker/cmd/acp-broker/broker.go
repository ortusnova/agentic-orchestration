package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/agentproc"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/registry"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/secrets"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/sortieclient"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/transport"
)

// agentStopGrace bounds how long the agent may take to exit after the
// broker is asked to stop, before its process group is killed.
const agentStopGrace = 5 * time.Second

// finalReleaseBound bounds the release the broker sends Sortie on exit.
const finalReleaseBound = 3 * time.Second

// brokerFlags are the options of the broker command.
type brokerFlags struct {
	key, socket, logPath, registryDir    string
	escalateAfter, permTimeout, handback time.Duration
	httpAddr, tokenFile, publicURL       string
	sourceURL                            string
	cookieTTL                            time.Duration
	replayEvents, replayBytes            int

	otlpEndpoint, otlpHeadersFile string
	telemetryContent              bool
	telemetryContentMax           int
}

func parseBrokerFlags(args []string) (brokerFlags, []string, error) {
	var f brokerFlags
	fs := flag.NewFlagSet("acp-broker", flag.ContinueOnError)
	fs.StringVar(&f.key, "key", "", "registry key (default: SORTIE_ISSUE_IDENTIFIER, else the base name of the working directory)")
	fs.StringVar(&f.registryDir, "registry-dir", "", "registry directory for records and sockets (default: $XDG_RUNTIME_DIR/acp-broker)")
	fs.StringVar(&f.socket, "socket", "", "viewer socket path (default: KEY.PID.sock in the registry directory)")
	fs.StringVar(&f.logPath, "log", "", "log file path (default: KEY.log in the registry directory)")
	fs.DurationVar(&f.escalateAfter, "escalate-after", broker.DefaultEscalateAfter, "how long a permission request waits for a viewer before Sortie is asked to call someone")
	fs.DurationVar(&f.permTimeout, "permission-timeout", broker.DefaultAnswerTimeout, "how long a permission request waits for an answer before the driver decides (0 waits without a bound)")
	fs.DurationVar(&f.handback, "handback-grace", broker.DefaultHandbackGrace, "how long humans keep the floor after the last viewer leaves")
	fs.StringVar(&f.httpAddr, "http", "", "HTTP listen address for web and phone viewers, such as 127.0.0.1:7700 (default: no HTTP listener)")
	fs.StringVar(&f.tokenFile, "viewer-token-file", "", "file holding the viewer token the HTTP listener requires")
	fs.StringVar(&f.publicURL, "public-url", "", "URL people use to reach the HTTP listener; links escalations and makes the session cookie Secure when it is https")
	fs.DurationVar(&f.cookieTTL, "cookie-ttl", transport.DefaultCookieTTL, "lifetime of a browser session cookie")
	fs.StringVar(&f.sourceURL, "source-url", transport.DefaultSourceURL, "where people can get the source of this broker, served on GET /source; set it to yours if you run a modified broker")
	fs.IntVar(&f.replayEvents, "replay-events", broker.DefaultReplayEvents, "events kept for viewers that attach late")
	fs.IntVar(&f.replayBytes, "replay-bytes", broker.DefaultReplayBytes, "bytes of events kept for viewers that attach late")
	fs.StringVar(&f.otlpEndpoint, "otlp-endpoint", "", "OTLP/HTTP endpoint, such as http://127.0.0.1:4318, to export each turn as a trace to (default: no telemetry)")
	fs.StringVar(&f.otlpHeadersFile, "otlp-headers-file", "", "file of \"Name: value\" lines sent as headers with every export, such as credentials for the collector")
	fs.BoolVar(&f.telemetryContent, "telemetry-content", false, "also export prompt text, agent messages, and tool input and output, masked and cut to -telemetry-content-max")
	fs.IntVar(&f.telemetryContentMax, "telemetry-content-max", telemetry.DefaultContentMax, "bytes of each transcript value exported with -telemetry-content")
	if err := fs.Parse(args); err != nil {
		return f, nil, err
	}
	return f, fs.Args(), nil
}

func runBroker(args []string) int {
	f, agentArgv, err := parseBrokerFlags(args)
	if err != nil {
		return 2
	}
	if len(agentArgv) == 0 {
		fmt.Fprintln(os.Stderr, "acp-broker: missing agent command after --")
		return 2
	}
	session := sortieclient.SessionFromEnv(os.Getenv)
	// A credential check only relays: it registers nothing and serves no
	// viewer, so it never races the work session for a name.
	passThrough := session.CredentialCheck()

	telCfg, err := telemetryFromFlags(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 2
	}

	var viewerToken string
	if f.httpAddr != "" && !passThrough {
		token, err := readToken(f.tokenFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "acp-broker: -http needs a viewer token: set -viewer-token-file:", err)
			return 2
		}
		viewerToken = token
	}
	if f.key == "" {
		cwd, _ := os.Getwd()
		f.key = registry.KeyFor(session.Identifier, cwd)
	}
	if f.registryDir == "" {
		f.registryDir = registry.Dir()
	}
	if f.socket == "" {
		f.socket = registry.SocketPath(f.registryDir, f.key, os.Getpid())
	}
	if f.logPath == "" {
		f.logPath = filepath.Join(f.registryDir, f.key+".log")
	}
	if err := os.MkdirAll(filepath.Dir(f.logPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 1
	}
	logFile, err := os.OpenFile(f.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 1
	}
	defer logFile.Close() //nolint:errcheck // the log is append-only; a close error has no reader
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: secrets.ReplaceAttr}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	var ln net.Listener
	if !passThrough {
		ln, err = transport.ListenUnix(ctx, f.socket)
		if err != nil {
			fmt.Fprintln(os.Stderr, "acp-broker:", err)
			return 1
		}
		defer ln.Close() //nolint:errcheck // closing the listener only stops accepting viewers
	}

	var sortie *sortieclient.Client
	if session.Supervised() {
		cfg := session.Config()
		cfg.Logger = logger
		sortie = sortieclient.New(cfg)
		sortieCtx, stopSortie := context.WithCancel(context.Background())
		go sortie.Run(sortieCtx)
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), finalReleaseBound)
			defer cancel()
			sortie.Close(closeCtx, stopSortie)
		}()
		logger.Info("reporting to sortie", slog.String("issue", session.Identifier), slog.String("api", session.APIURL))
	}

	// A credential check exports nothing: it is not a session anyone
	// works in.
	var recorder *telemetry.Recorder
	if telCfg != nil && !passThrough {
		tel := startTelemetry(telCfg, session, logger)
		defer tel.finish()
		recorder = tel.recorder
	}

	agent, err := startAgent(ctx, agentArgv, sortieclient.AgentEnv(os.Environ()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 1
	}
	logger.Info("broker started", slog.String("key", f.key), slog.Bool("pass_through", passThrough), slog.Int("agent_pid", agent.PID), slog.Any("agent", agentArgv))

	rec := registry.Record{
		Key: f.key, PID: os.Getpid(), Socket: f.socket,
		Issue: session.Identifier, Rule: session.Rule, ChainID: session.ChainID, Attempt: session.Attempt,
		StartedAt: time.Now().UTC(),
	}
	records := make(chan broker.SessionInfo, 16)
	var supervisor broker.Supervisor
	if sortie != nil {
		supervisor = sortieSupervisor{sortie}
	}
	b := broker.New(broker.Config{
		Supervisor: supervisor,
		DriverIn:   os.Stdin,
		DriverOut:  os.Stdout,
		AgentIn:    agent.Stdin,
		AgentOut:   agent.Stdout,
		Logger:     logger,
		OnSession: func(info broker.SessionInfo) {
			select {
			case records <- info:
			default:
			}
		},

		PassThrough:   passThrough,
		EscalateAfter: f.escalateAfter,
		AnswerTimeout: f.permTimeout,
		PublicURL:     f.publicURL,
		HandbackGrace: f.handback,
		ReplayEvents:  f.replayEvents,
		ReplayBytes:   f.replayBytes,
		Telemetry:     recorder,
	})

	if !passThrough {
		go func() { _ = transport.ServeUnix(ln, b) }()
		if f.httpAddr != "" {
			srv, err := serveHTTP(ctx, b, f.httpAddr, transport.HTTPConfig{
				Token: viewerToken, PublicURL: f.publicURL, CookieTTL: f.cookieTTL, SourceURL: f.sourceURL, Logger: logger,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "acp-broker:", err)
				return 1
			}
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
			}()
			rec.HTTPURL = cmp.Or(f.publicURL, "http://"+srv.Addr)
			logger.Info("http viewers listening", slog.String("addr", srv.Addr))
			// The listener is up: tell Sortie where viewers find this
			// session. A failure only logs; the session runs either way.
			if sortie != nil {
				go sortie.ReportBroker(ctx, rec.HTTPURL, viewerToken)
			}
		}
		if err := registry.Write(f.registryDir, rec); err != nil {
			logger.Warn("registry record not written", slog.Any("error", err))
		}
		kept := make(chan struct{})
		go func() {
			defer close(kept)
			keepRecord(b, f.registryDir, rec, records, logger)
		}()
		defer func() {
			<-kept
			registry.Remove(f.registryDir, f.key, os.Getpid())
		}()
	}

	runErr := b.Run(ctx)
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		logger.Warn("broker stopped", slog.Any("error", runErr))
	}
	code := agent.wait()
	logger.Info("agent exited", slog.Int("exit_code", code))
	return code
}

// keepRecord rewrites the registry record as the session ID and the agent
// become known, until the broker stops.
func keepRecord(b *broker.Broker, dir string, rec registry.Record, updates <-chan broker.SessionInfo, logger *slog.Logger) {
	for {
		select {
		case <-b.Done():
			return
		case info := <-updates:
			rec.SessionID = info.SessionID
			rec.Agent = registry.Agent{Name: info.AgentName, Version: info.AgentVersion}
			if err := registry.Write(dir, rec); err != nil {
				logger.Warn("registry record not updated", slog.Any("error", err))
			}
		}
	}
}

// agentProcess is the launched agent, and the copy of its standard error
// to the broker's.
type agentProcess struct {
	*agentproc.Process
	stderr chan struct{}
}

// startAgent launches argv in its own process group with env, copying its
// standard error to the broker's. Cancelling ctx stops it gracefully, then
// kills its process group after agentStopGrace.
func startAgent(ctx context.Context, argv, env []string) (*agentProcess, error) {
	p, err := agentproc.Start(ctx, argv, env, agentStopGrace)
	if err != nil {
		return nil, err
	}
	a := &agentProcess{Process: p, stderr: make(chan struct{})}
	go func() {
		defer close(a.stderr)
		_, _ = io.Copy(os.Stderr, p.Stderr)
	}()
	return a, nil
}

// wait reaps the agent and returns its exit code.
func (a *agentProcess) wait() int {
	<-a.Done()
	select {
	case <-a.stderr:
	case <-time.After(agentproc.DrainGrace):
	}
	_ = a.Close()
	return a.ExitCode()
}

// sortieSupervisor reports the broker's holds and escalations to Sortie.
type sortieSupervisor struct{ c *sortieclient.Client }

func (s sortieSupervisor) SetHold(reason string) { s.c.SetHold(reason) }

func (s sortieSupervisor) Escalate(kind, message, url string) {
	s.c.Escalate(sortieclient.Escalation{Kind: kind, Message: message, URL: url})
}

// readToken reads a viewer token from path, ignoring surrounding space.
func readToken(path string) (string, error) {
	if path == "" {
		return "", errors.New("no token file given")
	}
	data, err := os.ReadFile(path) //nolint:gosec // the operator names the token file
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return token, nil
}

// serveHTTP starts the HTTP viewer transport on addr.
func serveHTTP(ctx context.Context, b *broker.Broker, addr string, cfg transport.HTTPConfig) (*http.Server, error) {
	h, err := transport.NewHTTP(b, cfg)
	if err != nil {
		return nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Addr: ln.Addr().String(), Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, nil
}
