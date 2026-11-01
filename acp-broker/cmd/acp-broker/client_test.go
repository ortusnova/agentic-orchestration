package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

func TestPrintEvents_MarksHistoryThenLive(t *testing.T) {
	t.Parallel()

	chunk := func(seq int64, at time.Time, text string) broker.Event {
		update, _ := json.Marshal(map[string]any{"update": map[string]any{
			"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}})
		return broker.Event{Seq: seq, Time: at, Type: "update", Update: update}
	}
	var in bytes.Buffer
	for _, ev := range []broker.Event{
		{Type: "state", Floor: broker.FloorDriver, DriverTurn: "active", SessionID: "s1", Time: time.Now()},
		chunk(1, time.Now().Add(-time.Minute), "earlier"),
		chunk(2, time.Now().Add(time.Minute), "now"),
	} {
		line, _ := json.Marshal(ev)
		in.Write(append(line, '\n'))
	}

	var out bytes.Buffer
	printEvents(&in, &out)
	got := out.String()
	state, history, earlier := strings.Index(got, "[state]"), strings.Index(got, "--- history ---"), strings.Index(got, "earlier")
	live, now := strings.Index(got, "--- live ---"), strings.Index(got, "now")
	inOrder := state >= 0 && state < history && history < earlier && earlier < live && live < now
	if !inOrder {
		t.Errorf("printEvents output out of order:\n%s", got)
	}
}
