package broker

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

// Config wires a broker to its three parties.
type Config struct {
	// DriverIn and DriverOut carry the driving client's side of the
	// protocol: the broker reads its requests from DriverIn and writes the
	// agent's messages to DriverOut.
	DriverIn  io.Reader
	DriverOut io.Writer

	// AgentIn and AgentOut carry the agent's side: the broker writes to the
	// agent's standard input and reads its standard output.
	AgentIn  io.WriteCloser
	AgentOut io.Reader

	// EscalateAfter is how long a permission request waits for a
	// viewer's answer before the broker asks its supervisor to call
	// someone. A request that arrives with no viewer attached is escalated
	// at once. Zero selects DefaultEscalateAfter.
	EscalateAfter time.Duration

	// AnswerTimeout is how long a permission request waits for any answer
	// before it goes to the driver, which decides. Zero waits without a
	// bound.
	AnswerTimeout time.Duration

	// PassThrough relays every permission request to the driver at once,
	// as a broker that serves no viewer must.
	PassThrough bool

	// PublicURL is where people reach this session's viewers. It links an
	// escalation to the session when set.
	PublicURL string

	// Supervisor is told when humans hold the session and when someone
	// must be called. Nil runs the broker on its own.
	Supervisor Supervisor

	// OnSession is called from the loop whenever the session ID or the
	// agent's name and version become known. It must return at once.
	OnSession func(SessionInfo)

	// HandbackGrace is how long humans keep the floor after the last
	// viewer leaves with no human prompt running. Zero selects
	// DefaultHandbackGrace.
	HandbackGrace time.Duration

	// ReplayEvents and ReplayBytes bound the replay buffer late viewers
	// receive. Zero selects DefaultReplayEvents and DefaultReplayBytes.
	ReplayEvents int
	ReplayBytes  int

	// Telemetry records the session's turns as trace spans. Nil records
	// nothing.
	Telemetry *telemetry.Recorder

	Logger *slog.Logger
}

// Supervisor is the orchestrator a broker reports to. Both methods must
// return at once.
type Supervisor interface {
	// SetHold asks for the run to be held with reason, or released when
	// reason is empty.
	SetHold(reason string)

	// Escalate asks for people to be called about the session. kind is
	// "permission" or "question".
	Escalate(kind, message, url string)
}

// SessionInfo is what the broker has learned about its session.
type SessionInfo struct {
	SessionID    string
	AgentName    string
	AgentVersion string

	// Model is the session's current model as the agent reports it in its
	// session configuration, empty when it reports none.
	Model string
}

// Hold reasons the broker gives its supervisor.
const (
	HoldInterrupt  = "interrupt"
	HoldSteer      = "steer"
	HoldPermission = "permission request"
)

// Defaults for the permission timers.
const (
	DefaultEscalateAfter = 30 * time.Second
	DefaultAnswerTimeout = 10 * time.Minute
)

// DefaultHandbackGrace is how long humans keep the floor once the last
// viewer has left.
const DefaultHandbackGrace = 60 * time.Second

// pendingPermission is an agent permission request waiting for a viewer.
type pendingPermission struct {
	line    []byte
	params  permissionParams
	timer   *time.Timer // the answer timeout; nil without a bound
	escTime *time.Timer // the escalation delay; nil once escalated

	escalated bool
}

type permissionParams struct {
	ToolCall struct {
		Title string `json:"title"`
	} `json:"toolCall"`
	Options []permissionOption `json:"options"`
}

// isQuestion reports whether the request is a question rather than a
// permission request: none of its options allows anything.
func (p permissionParams) isQuestion() bool {
	for _, opt := range p.Options {
		if opt.Kind == "allow_once" || opt.Kind == "allow_always" {
			return false
		}
	}
	return true
}

// describe returns the escalation kind and message for the request: the
// tool's title for a permission request, and the title followed by the
// option names for a question.
func (p permissionParams) describe() (kind, message string) {
	title := strings.TrimSpace(p.ToolCall.Title)
	if !p.isQuestion() {
		if title == "" {
			title = "The agent asks for permission."
		}
		return "permission", title
	}
	if title == "" {
		title = "The agent asks a question."
	}
	names := make([]string, 0, len(p.Options))
	for _, opt := range p.Options {
		names = append(names, cmp.Or(opt.Name, opt.OptionID))
	}
	if len(names) == 0 {
		return "question", title
	}
	return "question", title + " Options: " + strings.Join(names, ", ")
}

type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Broker relays one session. Construct with New and start with Run.
type Broker struct {
	cfg    Config
	log    *slog.Logger
	events chan event

	// done is closed when Run returns, so a post from a transport or a
	// timer never blocks on a loop that is gone.
	done     chan struct{}
	doneOnce sync.Once

	// Everything below is owned by the loop goroutine.
	floor     Floor
	sessionID string
	steering  bool

	// driverPending maps the ID of each driver request the agent has not yet
	// answered to its method.
	driverPending map[string]string

	// activePrompt is the ID of the driver's session/prompt the agent is
	// running, empty when none is.
	activePrompt string

	// held is the ID of the driver's session/prompt whose turn a viewer took
	// over. The broker answers it on handback.
	held string

	// queued is a driver session/prompt that arrived while a viewer held the
	// session. It is forwarded on handback.
	queued *message

	// steers are viewer steers not yet delivered, in arrival order.
	steers []string

	// nativeBatch is the steers sent in a native steering request that
	// awaits the agent's outcome.
	nativeBatch []string

	// noNative reports that the queued steers already bounced off native
	// steering, so they wait for a turn of their own.
	noNative bool

	// withheld is the agent's answer to the driver's turn, kept from the
	// driver until the steers pending when that turn ended have landed.
	withheld []byte

	// driverCancelled is the ID of a driver prompt the driver itself
	// cancelled. Its answer goes to the driver even after a takeover.
	driverCancelled string

	// ownPending maps IDs of the broker's own requests to the viewer
	// operation that sent them.
	ownPending map[string]string
	nextID     int

	// permissions holds agent permission requests waiting for a viewer,
	// keyed by request ID.
	permissions map[string]*pendingPermission

	viewers map[*Viewer]struct{}

	// graceTimer hands the floor back when it fires; graceGen tells a
	// current firing from one a later change superseded.
	graceTimer *time.Timer
	graceGen   int

	// droppedViewer reports that the event at hand disconnected a viewer
	// that fell behind.
	droppedViewer bool

	// holdReason is the hold last reported to the supervisor, empty when
	// it was told to release.
	holdReason string

	// info is the session information last reported through OnSession.
	info                    SessionInfo
	agentName, agentVersion string
	model                   string

	evlog *eventLog
}

type eventKind int

const (
	evDriverLine eventKind = iota
	evDriverClosed
	evAgentLine
	evAgentClosed
	evViewerJoined
	evViewerLeft
	evViewerCommand
	evPermissionTimeout
	evPermissionEscalate
	evHandbackGrace
)

type event struct {
	kind       eventKind
	key        string
	line       []byte
	err        error
	viewer     *Viewer
	command    Command
	badCommand string

	// reply receives a command's immediate result instead of the
	// viewer's stream.
	reply chan Event

	// resume, after, and ready describe a joining viewer: whether it
	// resumes after a sequence number, and the channel closed once it is
	// registered.
	resume bool
	after  int64
	ready  chan struct{}

	// gen is the grace generation a evHandbackGrace belongs to.
	gen int
}

// New returns a broker for cfg.
func New(cfg Config) *Broker {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Broker{
		cfg:           cfg,
		log:           log,
		events:        make(chan event, 64),
		done:          make(chan struct{}),
		floor:         FloorDriver,
		driverPending: map[string]string{},
		ownPending:    map[string]string{},
		permissions:   map[string]*pendingPermission{},
		viewers:       map[*Viewer]struct{}{},
		evlog:         newEventLog(cfg.ReplayEvents, cfg.ReplayBytes),
	}
}

// post hands ev to the loop, or drops it once the loop has returned.
func (b *Broker) post(ev event) {
	select {
	case b.events <- ev:
	case <-b.done:
	}
}

// Done is closed when Run has returned.
func (b *Broker) Done() <-chan struct{} { return b.done }

// Run relays until the driver closes its input or the agent closes its
// output. Closing the driver's input closes the agent's input, so the agent
// can exit on its own.
func (b *Broker) Run(ctx context.Context) error {
	defer b.doneOnce.Do(func() { close(b.done) })
	// Turns still open when the broker stops end as unfinished.
	defer b.cfg.Telemetry.Stop()
	go b.readLines(b.cfg.DriverIn, evDriverLine, evDriverClosed)
	go b.readLines(b.cfg.AgentOut, evAgentLine, evAgentClosed)

	driverOut := bufio.NewWriter(b.cfg.DriverOut)
	agentIn := b.cfg.AgentIn
	toDriver := func(line []byte) {
		if _, err := driverOut.Write(line); err == nil {
			_ = driverOut.Flush()
		}
	}
	toAgent := func(line []byte) {
		if _, err := agentIn.Write(line); err != nil {
			b.log.Warn("write to agent failed", slog.Any("error", err))
		}
	}

	for {
		select {
		case <-ctx.Done():
			_ = agentIn.Close()
			b.closeViewers()
			return ctx.Err()
		case ev := <-b.events:
			if ev.kind == evAgentClosed {
				b.log.Info("agent closed its output", slog.Any("error", ev.err))
				b.closeViewers()
				return nil
			}
			if ev.kind == evDriverClosed {
				b.log.Info("driver closed its input", slog.Any("error", ev.err))
				_ = agentIn.Close()
				continue
			}
			b.handle(ev, toDriver, toAgent)
			b.deliverSteers(toAgent)
			b.releaseDriver(toDriver, toAgent)
			if b.droppedViewer {
				b.droppedViewer = false
				b.viewerGone()
			}
			b.syncHold()
			b.syncInfo()
		}
	}
}

// handle applies one event on the loop.
func (b *Broker) handle(ev event, toDriver, toAgent func([]byte)) {
	switch ev.kind {
	case evDriverLine:
		b.fromDriver(ev.line, toDriver, toAgent)
	case evAgentLine:
		b.fromAgent(ev.line, toDriver)
	case evViewerJoined:
		b.join(ev)
	case evViewerLeft:
		if _, ok := b.viewers[ev.viewer]; ok {
			delete(b.viewers, ev.viewer)
			close(ev.viewer.out)
		}
		b.viewerGone()
	case evHandbackGrace:
		if ev.gen == b.graceGen && len(b.viewers) == 0 && b.floor == FloorHuman && !b.humanTurnRunning() {
			b.log.Info("no viewer returned within the handback grace; handing back")
			b.handback(toDriver, toAgent)
		}
	case evPermissionTimeout:
		b.forwardPermission(ev.key, "nobody answered in time", toDriver)
	case evPermissionEscalate:
		b.escalatePermission(ev.key)
	case evViewerCommand:
		if ev.badCommand != "" {
			b.sendTo(ev.viewer, Event{Type: "error", Message: "bad command: " + ev.badCommand})
			return
		}
		b.onCommand(ev, toDriver, toAgent)
		// A command over HTTP may take the floor with nobody watching.
		b.armHandbackGrace()
	}
}

func (b *Broker) readLines(r io.Reader, lineKind, closedKind eventKind) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			b.post(event{kind: lineKind, line: line})
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			b.post(event{kind: closedKind, err: err})
			return
		}
	}
}

// wantedHold returns the hold the session's state calls for: humans on
// the floor, the driver kept waiting for steers, or a permission request
// waiting for an answer.
func (b *Broker) wantedHold() string {
	switch {
	case b.floor == FloorHuman:
		return HoldInterrupt
	case b.withheld != nil || b.ownOpRunning(opSteerTurn):
		return HoldSteer
	case len(b.permissions) > 0:
		return HoldPermission
	}
	return ""
}

// syncHold tells the supervisor about a change in the wanted hold.
func (b *Broker) syncHold() {
	want := b.wantedHold()
	if want == b.holdReason {
		return
	}
	held := b.holdReason != ""
	b.holdReason = want
	if b.cfg.Supervisor == nil {
		return
	}
	// A run already held stays held under its first reason; only a change
	// between held and released is news.
	if held && want != "" {
		return
	}
	b.cfg.Supervisor.SetHold(want)
}

// viewerGone reacts to a viewer leaving or being dropped. With nobody left,
// waiting permission requests go to the driver and the floor returns to
// the driver after the grace.
func (b *Broker) viewerGone() {
	if len(b.viewers) > 0 {
		return
	}
	// Nobody is left to answer here: call someone at once.
	for key := range b.permissions {
		b.escalatePermission(key)
	}
	b.armHandbackGrace()
}

// armHandbackGrace starts the grace after which the floor returns to the
// driver, when humans hold it, nobody watches, and no human turn runs.
func (b *Broker) armHandbackGrace() {
	if len(b.viewers) > 0 || b.floor != FloorHuman || b.humanTurnRunning() {
		return
	}
	b.stopHandbackGrace()
	grace := b.cfg.HandbackGrace
	if grace <= 0 {
		grace = DefaultHandbackGrace
	}
	gen := b.graceGen
	b.graceTimer = time.AfterFunc(grace, func() {
		b.post(event{kind: evHandbackGrace, gen: gen})
	})
}

// stopHandbackGrace cancels a pending grace.
func (b *Broker) stopHandbackGrace() {
	b.graceGen++
	if b.graceTimer != nil {
		b.graceTimer.Stop()
		b.graceTimer = nil
	}
}

// closeViewers disconnects every viewer.
func (b *Broker) closeViewers() {
	for v := range b.viewers {
		delete(b.viewers, v)
		close(v.out)
	}
}

// fromDriver routes one line the driving client sent.
func (b *Broker) fromDriver(line []byte, toDriver, toAgent func([]byte)) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		toAgent(line)
		return
	}
	switch {
	case m.isRequest():
		if m.Method == methodSessionPrompt && (b.floor == FloorHuman || b.driverWaits()) {
			// Humans hold the session, or steers must land first: keep the
			// driver's next turn rather than letting it interleave.
			b.queued = &m
			b.broadcast(b.stateEvent())
			return
		}
		b.driverPending[m.idKey()] = m.Method
		if m.Method == methodSessionPrompt {
			b.activePrompt = m.idKey()
			b.cfg.Telemetry.TurnStarted(driverTurnKey(m.idKey()), telemetry.OriginDriver, m.Params, 0)
			b.broadcast(b.stateEvent())
		}
		if m.Method == methodSessionLoad {
			b.sessionID = sessionIDFrom(m.Params)
		}
		toAgent(line)
	case m.isNotification() && m.Method == methodSessionCancel:
		// The driver is stopping its own turn. A turn the human holds or a
		// prompt queued behind it ends here as cancelled.
		if b.held != "" {
			toDriver(encode(message{ID: json.RawMessage(b.held), Result: stopReasonResult("cancelled")}))
			b.held = ""
		}
		if b.queued != nil {
			toDriver(encode(message{ID: b.queued.ID, Result: stopReasonResult("cancelled")}))
			b.queued = nil
		}
		// The driver's own cancel wins over steers it was kept waiting for:
		// it gets the answer at once, and the steers still land afterwards.
		if b.withheld != nil {
			toDriver(b.withheld)
			b.withheld = nil
		}
		// It also wins over an interrupt still waiting for this turn to end:
		// the driver gets the agent's answer.
		b.driverCancelled = b.activePrompt
		b.cancelPermissions(toAgent)
		toAgent(line)
		b.broadcast(b.stateEvent())
	default:
		toAgent(line)
	}
}

// fromAgent routes one line the agent sent.
func (b *Broker) fromAgent(line []byte, toDriver func([]byte)) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		toDriver(line)
		return
	}
	switch {
	case m.isResponse():
		key := m.idKey()
		if op, ok := b.ownPending[key]; ok {
			delete(b.ownPending, key)
			switch op {
			case opPrompt, opSteerTurn:
				b.cfg.Telemetry.TurnEnded(ownTurnKey(key), m.Result, m.Error)
			case opSteer:
				b.cfg.Telemetry.SteerAnswered(len(b.nativeBatch), m.Result, m.Error)
			}
			if op == opSteer && b.nativeSteerAnswered(m) {
				return
			}
			b.broadcast(Event{Type: "result", Op: op, Result: m.Result, Error: m.Error})
			b.armHandbackGrace()
			return
		}
		method := b.driverPending[key]
		delete(b.driverPending, key)
		switch method {
		case methodInitialize:
			b.steering = steeringAdvertised(m.Result)
			b.agentName, b.agentVersion = agentInfo(m.Result)
		case methodSessionNew:
			if id := sessionIDFrom(m.Result); id != "" {
				b.sessionID = id
			}
			b.noteModel(m.Result)
		case methodSessionLoad, methodSetConfigOption:
			b.noteModel(m.Result)
		case methodSessionPrompt:
			b.cfg.Telemetry.TurnEnded(driverTurnKey(key), m.Result, m.Error)
			if key == b.activePrompt {
				b.activePrompt = ""
				cancelledByDriver := key == b.driverCancelled
				b.driverCancelled = ""
				switch {
				case b.floor == FloorHuman && !cancelledByDriver:
					// The turn ended because a viewer interrupted it. Hold
					// the driver's request open until handback.
					b.held = key
					b.broadcast(b.stateEvent())
					return
				case !cancelledByDriver && b.steerPending():
					// Steers land before the driver gets control back: keep
					// the agent's answer until they have.
					b.withheld = line
					b.broadcast(b.stateEvent())
					return
				}
				b.broadcast(b.stateEvent())
			}
		}
		toDriver(line)
	case m.isNotification() && m.Method == methodSessionUpdate:
		var p struct {
			Update json.RawMessage `json:"update"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			if updateKind(p.Update) == updateConfigOption {
				b.noteModel(p.Update)
			}
			b.cfg.Telemetry.Update(p.Update)
		}
		b.broadcast(Event{Type: "update", Update: m.Params})
		if b.driverTurnOpen() {
			toDriver(line)
		}
	case m.isRequest() && m.Method == methodRequestPermission && !b.cfg.PassThrough:
		// Ask a person first; the driver decides only if nobody does.
		b.holdPermission(m, line)
	default:
		// Other agent requests go to the driver.
		toDriver(line)
	}
}

func (b *Broker) holdPermission(m message, line []byte) {
	key := m.idKey()
	p := &pendingPermission{line: line}
	_ = json.Unmarshal(m.Params, &p.params)
	if b.cfg.AnswerTimeout > 0 {
		p.timer = time.AfterFunc(b.cfg.AnswerTimeout, func() {
			b.post(event{kind: evPermissionTimeout, key: key})
		})
	}
	b.permissions[key] = p
	b.broadcast(Event{Type: "permission", ID: key, Request: m.Params})
	if len(b.viewers) == 0 {
		b.escalatePermission(key)
		return
	}
	after := b.cfg.EscalateAfter
	if after <= 0 {
		after = DefaultEscalateAfter
	}
	p.escTime = time.AfterFunc(after, func() {
		b.post(event{kind: evPermissionEscalate, key: key})
	})
}

// escalatePermission asks the supervisor, once, to call someone about a
// waiting request.
func (b *Broker) escalatePermission(key string) {
	p, ok := b.permissions[key]
	if !ok || p.escalated {
		return
	}
	p.escalated = true
	if p.escTime != nil {
		p.escTime.Stop()
		p.escTime = nil
	}
	kind, message := p.params.describe()
	b.log.Info("escalating a waiting permission request", slog.String("id", key), slog.String("kind", kind))
	if b.cfg.Supervisor != nil {
		b.cfg.Supervisor.Escalate(kind, message, b.cfg.PublicURL)
	}
}

// forwardPermission hands a waiting permission request to the driver.
func (b *Broker) forwardPermission(key, why string, toDriver func([]byte)) {
	p, ok := b.permissions[key]
	if !ok {
		return
	}
	b.dropPermission(key, p)
	toDriver(p.line)
	b.broadcast(Event{Type: "permission_resolved", ID: key, Message: "forwarded to driver: " + why})
}

// answerPermission answers a waiting permission request with outcome.
func (b *Broker) answerPermission(key string, outcome map[string]string, why string, toAgent func([]byte)) {
	p, ok := b.permissions[key]
	if !ok {
		return
	}
	b.dropPermission(key, p)
	result, _ := json.Marshal(map[string]any{"outcome": outcome})
	toAgent(encode(message{ID: json.RawMessage(key), Result: result}))
	b.broadcast(Event{Type: "permission_resolved", ID: key, Message: why})
}

func (b *Broker) dropPermission(key string, p *pendingPermission) {
	if p.timer != nil {
		p.timer.Stop()
	}
	if p.escTime != nil {
		p.escTime.Stop()
	}
	delete(b.permissions, key)
}

// cancelPermissions answers every waiting permission request as cancelled,
// as a client must when it cancels the turn that asked.
func (b *Broker) cancelPermissions(toAgent func([]byte)) {
	for key := range b.permissions {
		b.answerPermission(key, map[string]string{"outcome": "cancelled"}, "turn cancelled", toAgent)
	}
}

// permissionKey finds the waiting request a viewer named. Viewers may type
// a string ID without its JSON quotes.
func (b *Broker) permissionKey(id string) (string, bool) {
	for _, key := range []string{id, strconv.Quote(id)} {
		if _, ok := b.permissions[key]; ok {
			return key, true
		}
	}
	return "", false
}

// driverTurnOpen reports whether the driver has a turn running or held, or
// waits for its answer, so session updates belong to a turn it waits on.
func (b *Broker) driverTurnOpen() bool {
	return b.activePrompt != "" || b.held != "" || b.withheld != nil
}

func (b *Broker) onCommand(ev event, toDriver, toAgent func([]byte)) {
	cmd, v := ev.command, ev.viewer
	failed := false
	fail := func(msg string) {
		failed = true
		e := Event{Type: "error", Op: cmd.Op, Message: msg}
		if ev.reply != nil {
			ev.reply <- b.stamp(e)
			return
		}
		b.sendTo(v, e)
	}
	defer func() {
		if ev.reply != nil && !failed {
			ev.reply <- b.stamp(b.stateEvent())
		}
	}()
	if b.sessionID == "" && cmd.Op != "state" {
		fail("no session yet")
		return
	}
	switch cmd.Op {
	case "state":
		if v != nil {
			b.sendTo(v, b.stateEvent())
		}

	case "steer":
		if strings.TrimSpace(cmd.Text) == "" {
			fail("steer needs text")
			return
		}
		// A steer is never refused for the session's state: it joins the
		// queue and lands as soon as the session can take it.
		b.steers = append(b.steers, cmd.Text)
		b.deliverSteers(toAgent)
		b.broadcast(b.stateEvent())

	case "interrupt", "takeover", "cancel":
		// Interrupting always gives humans the floor; only a handback
		// returns it to the driver.
		b.floor = FloorHuman
		if b.turnRunning() {
			b.cfg.Telemetry.TurnInterrupted()
			b.cancelPermissions(toAgent)
			toAgent(b.cancelNotification())
		}
		b.broadcast(b.stateEvent())

	case "prompt":
		if b.floor != FloorHuman {
			fail("interrupt before prompting")
			return
		}
		if strings.TrimSpace(cmd.Text) == "" {
			fail("prompt needs text")
			return
		}
		if b.activePrompt != "" {
			fail("the driver's cancelled turn has not ended yet")
			return
		}
		if b.humanTurnRunning() {
			fail("a human turn is already running")
			return
		}
		b.sendPrompt(cmd.Op, []string{cmd.Text}, toAgent)

	case "permit":
		key, ok := b.permissionKey(cmd.ID)
		if !ok {
			fail("no waiting permission request with that id")
			return
		}
		if cmd.Option == "cancel" {
			b.answerPermission(key, map[string]string{"outcome": "cancelled"}, "cancelled by a viewer", toAgent)
			return
		}
		for _, opt := range b.permissions[key].params.Options {
			if opt.OptionID == cmd.Option || opt.Kind == cmd.Option {
				b.answerPermission(key, map[string]string{"outcome": "selected", "optionId": opt.OptionID}, "answered by a viewer: "+opt.OptionID, toAgent)
				return
			}
		}
		fail("no option matches " + strconv.Quote(cmd.Option))

	case "handback":
		if b.floor != FloorHuman {
			fail("not interrupted")
			return
		}
		if b.humanTurnRunning() {
			fail("a human turn is still running; interrupt it first")
			return
		}
		b.handback(toDriver, toAgent)

	default:
		fail("unknown op")
	}
}

// Operations the broker sends requests for on its own: a human prompt, a
// native steering request, and steers run as a turn of their own.
const (
	opPrompt    = "prompt"
	opSteer     = "steer"
	opSteerTurn = "steer-turn"
)

// humanTurnRunning reports whether a turn the broker started is running.
func (b *Broker) humanTurnRunning() bool {
	return b.ownOpRunning(opPrompt) || b.ownOpRunning(opSteerTurn)
}

// turnRunning reports whether the agent is running any turn.
func (b *Broker) turnRunning() bool {
	return b.activePrompt != "" || b.humanTurnRunning()
}

// steerPending reports whether a steer has yet to land: queued, awaiting a
// native outcome, or running as a turn.
func (b *Broker) steerPending() bool {
	return len(b.steers) > 0 || b.nativeBatch != nil || b.ownOpRunning(opSteerTurn)
}

// driverWaits reports whether the driver must wait for steers to land
// before its next turn starts.
func (b *Broker) driverWaits() bool {
	return b.withheld != nil || b.steerPending()
}

// ownOpRunning reports whether a request the broker sent for op awaits
// the agent's answer.
func (b *Broker) ownOpRunning(op string) bool {
	for _, pending := range b.ownPending {
		if pending == op {
			return true
		}
	}
	return false
}

// sendPrompt sends texts as one session/prompt for op, one text block each.
func (b *Broker) sendPrompt(op string, texts []string, toAgent func([]byte)) {
	params, _ := json.Marshal(map[string]any{"sessionId": b.sessionID, "prompt": textPrompt(texts...)})
	id := b.ownID(op)
	origin, steers := telemetry.OriginHuman, 0
	if op == opSteerTurn {
		origin, steers = telemetry.OriginSteer, len(texts)
	}
	b.cfg.Telemetry.TurnStarted(ownTurnKey(string(id)), origin, params, steers)
	toAgent(encode(message{ID: id, Method: methodSessionPrompt, Params: params}))
}

// driverTurnKey and ownTurnKey key turns for telemetry, keeping the
// driver's request IDs apart from the broker's own.
func driverTurnKey(id string) string { return "driver:" + id }

func ownTurnKey(id string) string { return "broker:" + id }

// deliverSteers lands the queued steers as soon as the session can take
// them: injected into a running turn when the agent supports it, or as a
// turn of their own once no turn runs. Every steer queued at that moment
// goes in one message.
func (b *Broker) deliverSteers(toAgent func([]byte)) {
	if len(b.steers) == 0 || b.nativeBatch != nil || b.sessionID == "" {
		return
	}
	if b.turnRunning() {
		if b.steering && !b.noNative {
			b.nativeBatch, b.steers = b.steers, nil
			b.sendNativeSteer(b.nativeBatch, toAgent)
		}
		return
	}
	batch := b.steers
	b.steers, b.noNative = nil, false
	b.sendPrompt(opSteerTurn, batch, toAgent)
	b.broadcast(b.stateEvent())
}

// sendNativeSteer injects texts into the running turn through the agent's
// steering extension, asking it to refuse rather than start a turn when
// none runs.
func (b *Broker) sendNativeSteer(texts []string, toAgent func([]byte)) {
	params, _ := json.Marshal(map[string]any{
		"sessionId": b.sessionID,
		"prompt":    textPrompt(texts...),
		"_meta":     map[string]any{"steering": map[string]string{"idleBehavior": "promptRequired"}},
	})
	toAgent(encode(message{ID: b.ownID(opSteer), Method: methodSteering, Params: params}))
}

// nativeSteerAnswered settles the native steer in flight and reports
// whether the broker keeps the answer to itself. An answer that no turn
// was running puts the steers back at the front of the queue to run as a
// turn of their own, so they are never lost and never sent natively twice.
func (b *Broker) nativeSteerAnswered(m message) bool {
	batch := b.nativeBatch
	b.nativeBatch = nil
	if len(batch) == 0 || !promptRequired(m.Result) {
		return false
	}
	b.log.Info("steer arrived after the turn ended; running it as a turn of its own")
	b.steers = append(batch, b.steers...)
	b.noNative = true
	return true
}

// promptRequired reports whether a steering result says no turn was
// running to take the steer.
func promptRequired(result json.RawMessage) bool {
	var r struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(result, &r)
	return r.Outcome == "promptRequired"
}

// releaseDriver gives the driver control back once its steers have landed:
// the withheld answer to its turn, then a prompt it queued meanwhile.
func (b *Broker) releaseDriver(toDriver, toAgent func([]byte)) {
	if b.floor != FloorDriver || b.steerPending() {
		return
	}
	changed := false
	if b.withheld != nil {
		toDriver(b.withheld)
		b.withheld = nil
		changed = true
	}
	if b.queued != nil {
		q := b.queued
		b.queued = nil
		b.driverPending[q.idKey()] = q.Method
		b.activePrompt = q.idKey()
		b.cfg.Telemetry.TurnStarted(driverTurnKey(q.idKey()), telemetry.OriginDriver, q.Params, 0)
		toAgent(encode(*q))
		changed = true
	}
	if changed {
		b.broadcast(b.stateEvent())
	}
}

// handback returns the floor to the driver: the held driver turn ends
// normally, or the answer withheld from it is passed on, and a driver
// prompt queued behind it goes to the agent once no steer is pending.
func (b *Broker) handback(toDriver, toAgent func([]byte)) {
	b.stopHandbackGrace()
	b.floor = FloorDriver
	if b.held != "" {
		toDriver(encode(message{ID: json.RawMessage(b.held), Result: stopReasonResult("end_turn")}))
		b.held = ""
	}
	b.releaseDriver(toDriver, toAgent)
	b.broadcast(b.stateEvent())
}

func (b *Broker) ownID(op string) json.RawMessage {
	b.nextID++
	id := strconv.Quote(fmt.Sprintf("acp-broker-%d", b.nextID))
	b.ownPending[id] = op
	return json.RawMessage(id)
}

func (b *Broker) cancelNotification() []byte {
	params, _ := json.Marshal(map[string]string{"sessionId": b.sessionID})
	return encode(message{Method: methodSessionCancel, Params: params})
}

func (b *Broker) stateEvent() Event {
	turn := "none"
	switch {
	case b.held != "" || b.withheld != nil:
		turn = "held"
	case b.activePrompt != "":
		turn = "active"
	}
	if b.queued != nil {
		turn += "+queued"
	}
	steering := b.steering
	return Event{
		Type: "state", Floor: b.floor, SessionID: b.sessionID, DriverTurn: turn, Steering: &steering,
		SteersQueued: len(b.steers) + len(b.nativeBatch),
	}
}

// stamp sets the time of an event that is not part of the log.
func (b *Broker) stamp(ev Event) Event {
	ev.Seq = 0
	ev.Time = time.Now().UTC()
	return ev
}

// broadcast numbers ev, keeps it in the replay buffer, and sends it to
// every viewer.
func (b *Broker) broadcast(ev Event) {
	ev.Seq = b.evlog.next()
	ev.Time = time.Now().UTC()
	rec := Record{Seq: ev.Seq, Data: encodeEvent(ev)}
	b.evlog.append(rec)
	for v := range b.viewers {
		b.sendRecord(v, rec)
	}
}

// sendTo sends ev to one viewer only, outside the event log.
func (b *Broker) sendTo(v *Viewer, ev Event) {
	if v == nil {
		return
	}
	b.sendRecord(v, Record{Data: encodeEvent(b.stamp(ev))})
}

func (b *Broker) sendRecord(v *Viewer, rec Record) {
	if _, ok := b.viewers[v]; !ok {
		return
	}
	select {
	case v.out <- rec:
	default:
		b.log.Warn("dropping a viewer that fell behind")
		delete(b.viewers, v)
		close(v.out)
		// The loop checks for an empty audience after the event at hand.
		b.droppedViewer = true
	}
}

// join registers a viewer and queues its replay: the state snapshot for a
// new viewer, a note on evicted events, and the buffered events.
func (b *Broker) join(ev event) {
	after := int64(0)
	if ev.resume {
		after = ev.after
	}
	records, dropped := b.evlog.since(after)
	v := ev.viewer
	v.out = make(chan Record, len(records)+viewerBuffer+2)
	b.viewers[v] = struct{}{}
	b.stopHandbackGrace()
	close(ev.ready)
	if !ev.resume {
		b.sendTo(v, b.stateEvent())
	}
	if dropped > 0 {
		b.sendTo(v, droppedEvent(after, dropped))
	}
	for _, rec := range records {
		b.sendRecord(v, rec)
	}
}

func sessionIDFrom(raw json.RawMessage) string {
	var s struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(raw, &s)
	return s.SessionID
}

// syncInfo reports newly learned session information.
func (b *Broker) syncInfo() {
	info := SessionInfo{SessionID: b.sessionID, AgentName: b.agentName, AgentVersion: b.agentVersion, Model: b.model}
	if info == b.info {
		return
	}
	b.info = info
	b.cfg.Telemetry.SetSession(info.SessionID, info.AgentName, info.AgentVersion)
	if b.cfg.OnSession != nil {
		b.cfg.OnSession(info)
	}
}

func agentInfo(raw json.RawMessage) (name, version string) {
	var r struct {
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.AgentInfo.Name, r.AgentInfo.Version
}

func steeringAdvertised(raw json.RawMessage) bool {
	var r struct {
		Meta struct {
			Steering struct {
				Supported bool `json:"supported"`
			} `json:"steering"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(raw, &r)
	return r.Meta.Steering.Supported
}

// noteModel takes the session's model from raw, an object that may carry
// the agent's session configuration options. A list without a model
// option means the agent reports none; no list leaves the model as is.
func (b *Broker) noteModel(raw json.RawMessage) {
	var r struct {
		ConfigOptions *[]struct {
			Category     string          `json:"category"`
			CurrentValue json.RawMessage `json:"currentValue"`
		} `json:"configOptions"`
	}
	if json.Unmarshal(raw, &r) != nil || r.ConfigOptions == nil {
		return
	}
	model := ""
	for _, opt := range *r.ConfigOptions {
		if opt.Category == "model" {
			_ = json.Unmarshal(opt.CurrentValue, &model)
			break
		}
	}
	b.model = model
	b.cfg.Telemetry.SetModel(model)
}

// updateConfigOption is the session update that carries the agent's
// changed session configuration options.
const updateConfigOption = "config_option_update"

// updateKind returns the sessionUpdate discriminator of a session update.
func updateKind(update json.RawMessage) string {
	var u struct {
		Kind string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(update, &u)
	return u.Kind
}
