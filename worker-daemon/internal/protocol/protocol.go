// Package protocol holds the wire types of worker API protocol 1
// (agent-platform/worker-api), as Sortie serves it under
// environment.kind remote, and the paths the daemon uses on each
// data-plane flow.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
)

// Version is the worker API protocol this daemon speaks.
const Version = 1

// Versions lists every worker API protocol version this daemon speaks.
var Versions = []int{Version}

// Header names.
const (
	// ProtocolHeader carries the protocol version on every request.
	ProtocolHeader = "X-Sortie-Worker-Protocol"
	// DefaultIdentityHeader names the worker when the configuration
	// names no other header.
	DefaultIdentityHeader = "X-Sortie-Worker-Identity"
)

// Routes of the control plane, under its URL.
const (
	CheckInPath = "/api/v1/workers/checkin"
	PollPath    = "/api/v1/workers/poll"
)

// AttemptPath returns the path of an attempt's heartbeat, report or
// resume route; action is "heartbeat", "report" or "resume".
func AttemptPath(job string, attempt int, action string) string {
	return "/api/v1/workers/jobs/" + url.PathEscape(job) + "/attempts/" + strconv.Itoa(attempt) + "/" + action
}

// Free is what the worker has free after its reserve and floor.
type Free struct {
	CPU     float64 `json:"cpu"`
	MemGiB  float64 `json:"mem_gib"`
	DiskGiB float64 `json:"disk_gib"`
}

// Declaration is the check-in body. Every field up to Free is
// required. Used and Unmanaged say what the workbench shows of the
// worker (agent-platform/run-supervision); a control plane that does not
// know them ignores them, as Sortie ignores every unknown member.
type Declaration struct {
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	KVM      bool     `json:"kvm"`
	Nested   bool     `json:"nested"`
	Backends []string `json:"backends"`
	RunSlots int      `json:"run_slots"`
	Running  int      `json:"running"`
	Free     Free     `json:"free"`
	// Used is what the running jobs were promised. Free plus Used is
	// what the worker can give jobs.
	Used Free `json:"used"`
	// Unmanaged lists the machines on this host that no Sortie started.
	Unmanaged []Unmanaged `json:"unmanaged"`
}

// Unmanaged is a machine on the worker that no Sortie started: neither
// this daemon for a job nor a Sortie on the host.
type Unmanaged struct {
	Name string `json:"name"`
	// Since is when the machine was created, as smolvm says, or else
	// when the daemon first saw it.
	Since time.Time `json:"since"`
	// State is smolvm's word for it, such as "running"; "" when unknown.
	State string `json:"state,omitempty"`
}

// Beat is the heartbeat body: the step the job reports, as the job last
// wrote it to its step file. Every member is omitted until the job
// reports a step.
type Beat struct {
	Step   string     `json:"step,omitempty"`
	Phase  string     `json:"phase,omitempty"`
	StepAt *time.Time `json:"step_at,omitempty"`
}

// Timings is the check-in answer.
type Timings struct {
	HeartbeatMS int64 `json:"heartbeat_ms"`
	LeaseMS     int64 `json:"lease_ms"`
}

// Requires is what a job needs of a worker.
type Requires struct {
	Capabilities []string `json:"capabilities"`
	CPU          float64  `json:"cpu"`
	MemGiB       float64  `json:"mem_gib"`
	DiskGiB      float64  `json:"disk_gib"`
}

// Issue names the issue a job runs.
type Issue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// Job is the job of an offer.
type Job struct {
	ID       string   `json:"id"`
	Attempt  int      `json:"attempt"`
	Priority *int     `json:"priority"`
	Issue    Issue    `json:"issue"`
	Requires Requires `json:"requires"`
}

// RunCredential names a placeholder the egress flow replaces. It never
// holds a value.
type RunCredential struct {
	Name        string   `json:"name"`
	Env         string   `json:"env"`
	Placeholder string   `json:"placeholder"`
	Hosts       []string `json:"hosts"`
	Header      string   `json:"header,omitempty"`
}

// Run is the run descriptor: what to start, never a credential.
type Run struct {
	Backend        string            `json:"backend"`
	Image          string            `json:"image"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	StallTimeoutMS int               `json:"stall_timeout_ms"`
	SeedPath       string            `json:"seed_path"`
	Credentials    []RunCredential   `json:"credentials"`
	// Egress is the job's egress policy (agent-platform/job-egress-policy),
	// kept as sent so that the daemon checks it strictly; absent, the
	// machine gets no network. See [Egress].
	Egress json.RawMessage `json:"egress,omitempty"`
}

// Egress is run.egress: the policies whose hosts the job's machine
// reaches directly, apart so that each host's source is known, and the
// hosts that stay on the egress flow.
type Egress struct {
	Source          *EgressSource `json:"source,omitempty"`
	Harness         *EgressPolicy `json:"harness,omitempty"`
	Repository      *EgressPolicy `json:"repository,omitempty"`
	CredentialHosts []string      `json:"credential_hosts,omitempty"`
}

// EgressSource names where Sortie read the repository's policy: the
// repository's name, its base branch, the commit read and the file.
type EgressSource struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
	File       string `json:"file"`
}

// EgressPolicy is one policy in the format of .sortie/egress.yaml. An
// offer carries it without its version, its lists empty, never null.
type EgressPolicy struct {
	// Version, when sent, is 1.
	Version *int            `json:"version,omitempty"`
	Hosts   []EgressHost    `json:"hosts"`
	UDP     []EgressUDP     `json:"udp"`
	Private []EgressPrivate `json:"private"`
}

// EgressHost is a host the machine reaches by name: a DNS name, or
// *. and a domain.
type EgressHost struct {
	Host   string `json:"host"`
	Reason string `json:"reason"`
}

// EgressUDP is UDP to a CIDR on a port or a range of ports.
type EgressUDP struct {
	CIDR   string     `json:"cidr"`
	Port   EgressPort `json:"port"`
	Reason string     `json:"reason"`
}

// EgressPrivate is a private range the machine reaches.
type EgressPrivate struct {
	CIDR   string `json:"cidr"`
	Reason string `json:"reason"`
}

// EgressPort is a UDP port, N, or a range, N-M. An offer carries it as
// a string, such as "123" or "5000-5010"; a number is read too.
type EgressPort string

// UnmarshalJSON reads a number or a string.
func (p *EgressPort) UnmarshalJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return err
	}
	switch v := v.(type) {
	case json.Number:
		*p = EgressPort(v.String())
	case string:
		*p = EgressPort(v)
	default:
		return fmt.Errorf("port %s is neither a number nor a string", b)
	}
	return nil
}

// FlowAccess is where a job reaches one flow, and the job token that
// admits it.
type FlowAccess struct {
	URL   string       `json:"url"`
	Token secret.Token `json:"token"`
}

// Lease is when an offer's lease ends unless renewed.
type Lease struct {
	Expires time.Time `json:"expires"`
}

// Offer is a poll's answer.
type Offer struct {
	Job   Job                   `json:"job"`
	Lease Lease                 `json:"lease"`
	Run   Run                   `json:"run"`
	Flows map[string]FlowAccess `json:"flows"`
}

// Flow names.
const (
	FlowStdio   = "stdio"
	FlowEgress  = "egress"
	FlowMCP     = "mcp"
	FlowBundles = "bundles"
	FlowUsage   = "usage"
)

// Flows lists every flow an offer carries.
var Flows = []string{FlowStdio, FlowEgress, FlowMCP, FlowBundles, FlowUsage}

// Report outcomes and machine states.
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"

	MachineRunning = "running"
	MachineStopped = "stopped"
	MachineDeleted = "deleted"
)

// Report is the report body.
type Report struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	Machine string `json:"machine"`
}

// ErrorBody is the control plane's error answer.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Supported []int `json:"supported,omitempty"`
}

// AttemptSuffix is the path, under a flow's URL, that names one
// attempt: the stdio stream, the usage report, and the root of the MCP
// endpoint use it.
func AttemptSuffix(job string, attempt int) string {
	return "/jobs/" + url.PathEscape(job) + "/attempts/" + strconv.Itoa(attempt)
}

// ResultPath is where, on the bundles flow, the daemon uploads the
// bundle of the run's commits.
func ResultPath(job string, attempt int) string {
	return AttemptSuffix(job, attempt) + "/result"
}

// SeedPath is where, on the bundles flow, the workspace seed of an
// attempt is fetched, when the run descriptor names no seed_path.
func SeedPath(job string, attempt int) string {
	return AttemptSuffix(job, attempt) + "/seed"
}

// Result parts and their limit.
const (
	// ResultBundlePart holds the bundle of the issue branch.
	ResultBundlePart = "bundle"
	// ResultPRPart holds the agent's .sortie/pr.json.
	ResultPRPart = "pr"
	// MaxPRFile bounds the pr part.
	MaxPRFile = 64 << 10
	// ResultStatusPart holds the agent's .sortie/status.
	ResultStatusPart = "status"
	// MaxStatusFile bounds the status part.
	MaxStatusFile = 4 << 10
	// ResultCriteriaPart holds a criteria review's .sortie/criteria.json,
	// its verdict on each acceptance criterion.
	ResultCriteriaPart = "criteria"
	// MaxCriteriaFile bounds the criteria part.
	MaxCriteriaFile = 64 << 10
)

// Usage is the body the daemon posts on the usage flow: the attempt's
// token totals so far. Measured false says spend occurred and its
// amount is unknown; the counts are then zero and mean nothing.
type Usage struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Model            string `json:"model"`
	Measured         bool   `json:"measured"`
}
