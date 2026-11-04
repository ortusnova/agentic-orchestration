package workflow

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The checks on p1's workflow that agent-platform/one-sortie asks of the
// kit: Sortie can only be run against it on p1 (run-sortie.sh validate).

// workflowFrontMatter is WORKFLOW.md's front matter, without comments.
func workflowFrontMatter(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(b), "---\n", 3)
	if len(parts) != 3 {
		t.Fatal("WORKFLOW.md has no front matter")
	}
	var lines []string
	for line := range strings.SplitSeq(parts[1], "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			lines = append(lines, line)
		}
	}
	return lines
}

// repositories maps each repository of environment.remote to its lines.
func repositories(t *testing.T, lines []string) (names []string, entries map[string][]string) {
	t.Helper()
	entries = map[string][]string{}
	name := regexp.MustCompile(`^      - name: ([a-z0-9-]+)$`)
	in, cur := false, ""
	for _, line := range lines {
		switch {
		case line == "    repositories:":
			in = true
			continue
		case in && !strings.HasPrefix(line, "      "):
			in = false
		}
		if !in {
			continue
		}
		if m := name.FindStringSubmatch(line); m != nil {
			cur = m[1]
			names = append(names, cur)
		}
		entries[cur] = append(entries[cur], strings.TrimSpace(line))
	}
	return names, entries
}

// One Sortie dispatches every repository the plan names, under the remote
// kind: p1's workflow lists sortie, agentic-orchestration and homelab, and
// closes none of them to dispatch, so no other Sortie has a story to run
// and no rule waits on another Sortie's merges.
func TestOneSortieDispatchesEveryRepository(t *testing.T) {
	lines := workflowFrontMatter(t)
	if !slices.Contains(lines, "environment:") || !slices.Contains(lines, "  kind: remote") || !slices.Contains(lines, "  remote:") {
		t.Error("WORKFLOW.md's environment is not the remote kind")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "smolvm:" || strings.Contains(line, "kind: smolvm") {
			t.Errorf("WORKFLOW.md still configures the co-located smolvm mode: %q", line)
		}
		if strings.Contains(line, "dispatch: false") {
			t.Errorf("WORKFLOW.md closes a repository to dispatch: %q", line)
		}
	}
	names, _ := repositories(t, lines)
	if want := []string{"sortie", "agentic-orchestration", "homelab"}; !slices.Equal(names, want) {
		t.Errorf("repositories = %q, want %q", names, want)
	}
	if !slices.Contains(lines, "    default_repository: sortie") {
		t.Error("a story that names no repository does not run in sortie")
	}
}

// Under the remote kind Sortie runs no hook: it refuses to prepare the
// workspace of every story whose workflow has one, so no story runs.
func TestTheRemoteKindHasNoHooks(t *testing.T) {
	for _, line := range workflowFrontMatter(t) {
		if line == "hooks:" || strings.HasPrefix(line, "hooks:") {
			t.Errorf("WORKFLOW.md has hooks, which the remote kind refuses: %q", line)
		}
	}
}

// Requirements say what a job needs, never a host name: the homelab job
// requires nested and the homelab sandbox's CPUs, memory and disk, and no
// requirement, image or flow names p1, taichi or any worker.
func TestRequirementsNameWhatAJobNeeds(t *testing.T) {
	lines := workflowFrontMatter(t)
	_, entries := repositories(t, lines)
	homelab := entries["homelab"]
	if !slices.Contains(homelab, "requires: {capabilities: [nested], cpu: 8, mem_gib: 20, disk_gib: 60}") {
		t.Errorf("homelab's requirements are not nested, 8 CPUs, 20 GiB and 60 GiB: %q", homelab)
	}
	if !slices.Contains(homelab, "image: homelab.smolmachine") {
		t.Errorf("homelab's jobs do not run the homelab pack: %q", homelab)
	}
	if !slices.Contains(lines, "    requires: {cpu: 4, mem_gib: 8, disk_gib: 20}") {
		t.Error("the default requirements are not the stage-0 pack's 4 CPUs, 8 GiB and 20 GiB")
	}
	req := regexp.MustCompile(`requires: \{(capabilities: \[[a-z0-9_, -]+\], )?(.*)\}`)
	size := regexp.MustCompile(`^(cpu|mem_gib|disk_gib): [0-9.]+$`)
	host := regexp.MustCompile(`\b(p1|taichi|localhost|host|hostname)\b`)
	for _, line := range lines {
		if m := req.FindStringSubmatch(line); m != nil {
			for f := range strings.SplitSeq(m[2], ", ") {
				if !size.MatchString(f) {
					t.Errorf("a requirement that is not a capability, CPU, memory or disk: %q", f)
				}
			}
		}
		l := strings.TrimSpace(line)
		if (strings.HasPrefix(l, "requires:") || strings.HasPrefix(l, "image:") || strings.Contains(l, "{url:")) && host.MatchString(l) {
			t.Errorf("a job's requirement, image or flow names a host: %q", l)
		}
	}
	if n := pomeriumFlows(lines); n != 5 {
		t.Errorf("%d of the five flows are at Pomerium's public URL, the same on every worker", n)
	}
}

// pomeriumFlows counts the flows the workflow names at Pomerium's public
// URL, each at the path of its route (control-plane/internal/pomerium):
// the stage-1 edge and its tunnels are gone (design D18).
func pomeriumFlows(lines []string) int {
	flow := regexp.MustCompile(`^      (stdio|egress|mcp|bundles|usage): \{url: "\$\{STAGE0_CP_PUBLIC_URL\}(/[a-z]+)?"\}$`)
	n := 0
	for _, line := range lines {
		m := flow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		want := "/" + m[1]
		if m[1] == "egress" {
			want = ""
		}
		if m[2] == want {
			n++
		}
	}
	return n
}

// No flow reaches the stage-1 edge, on a worker's loopback or anywhere:
// every worker, p1's own too, reaches Sortie through Pomerium.
func TestNoFlowReachesTheEdge(t *testing.T) {
	for _, line := range workflowFrontMatter(t) {
		if strings.Contains(line, "STAGE0_EDGE") || strings.Contains(line, "127.0.0.1:7700") {
			t.Errorf("WORKFLOW.md still names the stage-1 edge: %q", line)
		}
	}
}

// environment.remote holds only the keys the remote kind takes, so Sortie
// neither refuses the workflow nor warns of a key it ignores
// (unknown_sub_key): the harness's hosts are its egress, never allow_hosts,
// and IS_SANDBOX is the worker image's, not a pass_env.
func TestTheRemoteSectionHasOnlyRemoteKeys(t *testing.T) {
	known := []string{"backend", "image", "requires", "flows", "credentials", "egress", "repositories", "default_repository"}
	key := regexp.MustCompile(`^    ([a-z_]+):`)
	in := false
	for _, line := range workflowFrontMatter(t) {
		switch {
		case line == "  remote:":
			in = true
			continue
		case in && !strings.HasPrefix(line, "    "):
			in = false
		}
		if m := key.FindStringSubmatch(line); in && m != nil && !slices.Contains(known, m[1]) {
			t.Errorf("environment.remote.%s is not a key of the remote kind", m[1])
		}
	}
	keys := regexp.MustCompile(`^      ([a-z_]+):`)
	_, entries := repositories(t, workflowFrontMatter(t))
	repoKeys := []string{"name", "url", "base", "provider", "project", "image", "requires", "dispatch"}
	for name, lines := range entries {
		for _, l := range lines {
			l = "      " + strings.TrimPrefix(l, "- ")
			if m := keys.FindStringSubmatch(l); m != nil && !slices.Contains(repoKeys, m[1]) {
				t.Errorf("repository %s has %s, which a remote repository does not take", name, m[1])
			}
		}
	}
}
