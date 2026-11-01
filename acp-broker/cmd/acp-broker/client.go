package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/registry"
)

func runList(args []string) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	dir := fs.String("registry-dir", "", "registry directory (default: $XDG_RUNTIME_DIR/acp-broker)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	records, err := registry.List(cmp.Or(*dir, registry.Dir()), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 1
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "KEY\tPID\tISSUE\tRULE\tAGENT\tSESSION\tSTARTED\tHTTP\tSOCKET")
	for _, r := range records {
		agent := strings.TrimSpace(r.Agent.Name + " " + r.Agent.Version)
		_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Key, r.PID, dash(r.Issue), dash(r.Rule), dash(agent), dash(r.SessionID),
			r.StartedAt.Local().Format(time.TimeOnly), dash(r.HTTPURL), r.Socket)
	}
	_ = w.Flush()
	return 0
}

func dash(s string) string { return cmp.Or(s, "-") }

// target names the broker a viewer command connects to.
type target struct {
	socket string
	key    string
	dir    string
	follow bool
}

func parseTarget(args []string, name string) (target, []string, bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	socket := fs.String("socket", "", "viewer socket path")
	key := fs.String("key", "", "registry key (default: the base name of the working directory)")
	dir := fs.String("registry-dir", "", "registry directory (default: $XDG_RUNTIME_DIR/acp-broker)")
	var follow *bool
	if name == "attach" {
		follow = fs.Bool("follow", false, "move to the key's next session when this one ends")
	}
	if err := fs.Parse(args); err != nil {
		return target{}, nil, false
	}
	t := target{socket: *socket, key: *key, dir: cmp.Or(*dir, registry.Dir())}
	if t.key == "" {
		cwd, _ := os.Getwd()
		t.key = registry.KeyFor("", cwd)
	}
	if follow != nil {
		t.follow = *follow
	}
	return t, fs.Args(), true
}

func dialUnix(path string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return (&net.Dialer{}).DialContext(ctx, "unix", path)
}

// connect dials t's socket, or the newest live session for t's key whose
// socket is not in seen.
func (t target) connect(seen map[string]bool) (net.Conn, string, error) {
	if t.socket != "" {
		conn, err := dialUnix(t.socket)
		return conn, t.socket, err
	}
	records, err := registry.List(t.dir, t.key)
	if err != nil {
		return nil, "", err
	}
	for _, r := range records {
		if seen[r.Socket] {
			continue
		}
		conn, err := dialUnix(r.Socket)
		if err == nil {
			return conn, r.Socket, nil
		}
	}
	return nil, "", fmt.Errorf("no live session for key %q in %s", t.key, t.dir)
}

func dial(args []string, name string) (net.Conn, []string, int) {
	t, rest, ok := parseTarget(args, name)
	if !ok {
		return nil, nil, 2
	}
	conn, _, err := t.connect(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return nil, nil, 1
	}
	return conn, rest, 0
}

// runSend sends one command and prints the events that follow until the
// first result or error, or until the state event that answers it.
func runSend(args []string) int {
	conn, rest, code := dial(args, "send")
	if conn == nil {
		return code
	}
	defer conn.Close() //nolint:errcheck // the command is done with the connection
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "acp-broker send: missing OP")
		return 2
	}
	cmd := parseCommand(rest[0], rest[1:])
	line, _ := json.Marshal(cmd)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		fmt.Fprintln(os.Stderr, "acp-broker:", err)
		return 1
	}
	r := bufio.NewReader(conn)
	// The first line is the state event every new viewer receives.
	if first, err := r.ReadBytes('\n'); err == nil {
		_, _ = os.Stdout.Write(first)
	}
	for {
		next, err := r.ReadBytes('\n')
		if len(next) > 0 {
			var ev broker.Event
			_ = json.Unmarshal(next, &ev)
			if ev.Type != "update" {
				_, _ = os.Stdout.Write(next)
				if ev.Type == "error" || ev.Type == "state" || (ev.Type == "result" && ev.Op == cmd.Op) {
					return 0
				}
			}
		}
		if err != nil {
			return 0
		}
	}
}

// runAttach prints a readable stream of the session. Lines typed on
// standard input are commands: "/interrupt" (or "/takeover", "/cancel"),
// "/handback", "/state", "/steer TEXT", "/permit ID OPTION"; any other line
// is a prompt.
// With -follow it waits for the key's next session whenever one ends.
func runAttach(args []string) int {
	t, _, ok := parseTarget(args, "attach")
	if !ok {
		return 2
	}
	var mu sync.Mutex
	var current net.Conn
	go func() {
		in := bufio.NewScanner(os.Stdin)
		for in.Scan() {
			text := strings.TrimSpace(in.Text())
			if text == "" {
				continue
			}
			cmd := broker.Command{Op: "prompt", Text: text}
			if strings.HasPrefix(text, "/") {
				fields := strings.Fields(text[1:])
				cmd = parseCommand(fields[0], fields[1:])
			}
			line, _ := json.Marshal(cmd)
			mu.Lock()
			if current != nil {
				_, _ = current.Write(append(line, '\n'))
			}
			mu.Unlock()
		}
	}()

	seen := map[string]bool{}
	for {
		conn, path, err := t.connect(seen)
		if err != nil {
			if !t.follow {
				fmt.Fprintln(os.Stderr, "acp-broker:", err)
				return 1
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		seen[path] = true
		fmt.Printf("[attached] %s\n", filepath.Base(path))
		mu.Lock()
		current = conn
		mu.Unlock()
		printEvents(conn, os.Stdout)
		mu.Lock()
		current = nil
		mu.Unlock()
		_ = conn.Close()
		if !t.follow {
			return 0
		}
	}
}

// parseCommand builds a command from an op and its words. "permit" takes a
// request ID and an option; every other op takes free text.
func parseCommand(op string, words []string) broker.Command {
	if op == "permit" {
		cmd := broker.Command{Op: op}
		if len(words) > 0 {
			cmd.ID = words[0]
		}
		if len(words) > 1 {
			cmd.Option = words[1]
		}
		return cmd
	}
	return broker.Command{Op: op, Text: strings.Join(words, " ")}
}

// printEvents prints a session's events: its state, then the history the
// broker replays, then live events, with a marker where history ends.
func printEvents(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	inText := false
	attachedAt := time.Now()
	history, live := false, false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var ev broker.Event
			if json.Unmarshal(line, &ev) == nil {
				switch {
				case ev.Seq > 0 && !history && ev.Time.Before(attachedAt):
					history = true
					_, _ = fmt.Fprintln(w, "\n--- history ---")
					inText = false
				case ev.Seq > 0 && !live && !ev.Time.Before(attachedAt):
					live = true
					_, _ = fmt.Fprintln(w, "\n--- live ---")
					inText = false
				}
				inText = printEvent(w, ev, inText)
			}
		}
		if err != nil {
			_, _ = fmt.Fprintln(w, "\n[detached]")
			return
		}
	}
}

func printEvent(w io.Writer, ev broker.Event, inText bool) bool {
	if ev.Type == "update" {
		var p struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Title   string `json:"title"`
				Status  string `json:"status"`
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		_ = json.Unmarshal(ev.Update, &p)
		switch p.Update.Kind {
		case "agent_message_chunk", "user_message_chunk":
			if !inText {
				prefix := "agent> "
				if p.Update.Kind == "user_message_chunk" {
					prefix = "user> "
				}
				_, _ = fmt.Fprint(w, "\n"+prefix)
			}
			_, _ = fmt.Fprint(w, p.Update.Content.Text)
			return true
		case "tool_call":
			_, _ = fmt.Fprintf(w, "\n[tool] %s\n", p.Update.Title)
		case "tool_call_update":
			if p.Update.Status != "" {
				_, _ = fmt.Fprintf(w, "[tool %s]\n", p.Update.Status)
			}
		}
		return false
	}
	_, _ = fmt.Fprintf(w, "\n[%s] ", ev.Type)
	switch ev.Type {
	case "state":
		_, _ = fmt.Fprintf(w, "floor=%s driverTurn=%s session=%s", ev.Floor, ev.DriverTurn, ev.SessionID)
	case "result":
		_, _ = fmt.Fprintf(w, "%s %s%s", ev.Op, ev.Result, ev.Error)
	case "error":
		_, _ = fmt.Fprintf(w, "%s %s", ev.Op, ev.Message)
	case "permission":
		var req struct {
			ToolCall struct {
				Title string `json:"title"`
			} `json:"toolCall"`
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
			} `json:"options"`
		}
		_ = json.Unmarshal(ev.Request, &req)
		_, _ = fmt.Fprintf(w, "id=%s %q options:", ev.ID, req.ToolCall.Title)
		for _, o := range req.Options {
			_, _ = fmt.Fprintf(w, " %s(%s)", o.OptionID, o.Kind)
		}
		_, _ = fmt.Fprintf(w, "\n  answer with: /permit %s allow_once", ev.ID)
	case "permission_resolved":
		_, _ = fmt.Fprintf(w, "id=%s %s", ev.ID, ev.Message)
	}
	_, _ = fmt.Fprintln(w)
	return false
}
