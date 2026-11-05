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
// (unknown_sub_key): the allowed hosts are nono's (egress-profile.json),
// and IS_SANDBOX is the worker image's, not a pass_env.
func TestTheRemoteSectionHasOnlyRemoteKeys(t *testing.T) {
	known := []string{"backend", "image", "requires", "flows", "credentials", "repositories", "default_repository", "profiles"}
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
	// A job profile takes an image and its requires, nothing else.
	profileKey := regexp.MustCompile(`^        ([a-z_]+):`)
	in = false
	for _, line := range workflowFrontMatter(t) {
		switch {
		case line == "    profiles:":
			in = true
			continue
		case in && !strings.HasPrefix(line, "      "):
			in = false
		}
		if m := profileKey.FindStringSubmatch(line); in && m != nil && m[1] != "image" && m[1] != "requires" {
			t.Errorf("a job profile has %s, which Sortie does not take", m[1])
		}
	}
}

// reviewOnPreviewPrompt is the prompt of the review on a ui story's preview:
// the branch of WORKFLOW.md's template that the kit's preview comment opens.
func reviewOnPreviewPrompt(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "You are reviewing story **{{ .issue.identifier }}** on its preview")
	if start < 0 {
		t.Fatal("WORKFLOW.md has no review-on-preview prompt")
	}
	end := strings.Index(s[start:], "{{ else -}}")
	if end < 0 {
		t.Fatal("the review-on-preview prompt does not end")
	}
	return strings.Join(strings.Fields(s[start:start+end]), " ")
}

// agent-platform/run-supervision#A UI is approved as a prototype, and the
// built story merges after its review, scenario The review machine reaches
// the preview: the review opens the commented address from its own
// machine, through its proxy and the control plane's front, and an address
// that does not answer is a platform fault it stops on, never a request to
// a person to redeploy the preview.
func TestTheReviewOpensThePreviewThroughTheFront(t *testing.T) {
	p := reviewOnPreviewPrompt(t)
	for _, want := range []string{
		"Open the preview's address, exactly as the kit's comment gives it",
		"with this machine's proxy settings",
		"proxy: { server: process.env.HTTP_PROXY }",
		"through the control plane's front with this job's token",
		"When the address does not answer, or answers an error, that is a platform fault: stop with `blocked` in `.sortie/status`, and give on the lines after it the address and the error you got.",
		"Never ask a person to redeploy the preview: the kit redeploys one that does not answer on its own.",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("the review-on-preview prompt does not say %q", want)
		}
	}
	for _, never := range []string{"127.0.0.1", "localhost"} {
		if strings.Contains(p, never) {
			t.Errorf("the review-on-preview prompt names %s, which is not the preview's address on the review's machine", never)
		}
	}
}

// agent-platform/supervisor-autonomy#A change provable only after deploy is
// merged, proven, or reverted by Sortie: the supervisor's rule 2 merges a
// story whose only unmet criteria are after-deploy live criteria with
// merge-and-prove, never with merge, and the review marks such a criterion
// live-proof with the reason after-deploy. Sortie's side of both, with the
// kit's texts, is sortie-test/stage0_deploy_test.go.
func TestAnAfterDeployStoryGoesThroughMergeAndProve(t *testing.T) {
	raw, err := os.ReadFile("../supervisor.md")
	if err != nil {
		t.Fatal(err)
	}
	supervisor := strings.Join(strings.Fields(string(raw)), " ")
	start := strings.Index(supervisor, "2. **Merge only what passed.**")
	end := strings.Index(supervisor, "3. **Never rewrite a pushed commit")
	if start < 0 || end < start {
		t.Fatal("supervisor.md has no rule 2")
	}
	if want := "A pull request whose only unmet criteria are after-deploy live criteria is merged with merge-and-prove once its other criteria are met and CI passes, never with merge; Sortie proves or reverts it."; !strings.Contains(supervisor[start:end], want) {
		t.Errorf("supervisor.md's rule 2 does not say %q", want)
	}
	if !strings.Contains(supervisor, "`POST /api/v1/<identifier>/merge-and-prove` instead, never with `merge`") {
		t.Error("supervisor.md's actions do not name the merge-and-prove route")
	}
	b, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	review := string(b)
	start = strings.Index(review, `{{- else if eq .stage.current "review" -}}`)
	end = strings.Index(review, "{{- else -}}")
	if start < 0 || end < start {
		t.Fatal("WORKFLOW.md has no review prompt")
	}
	review = strings.Join(strings.Fields(review[start:end]), " ")
	for _, want := range []string{
		"is an after-deploy criterion",
		"Record it `live-proof` with the reason `after-deploy`",
		`"verdict": "live-proof", "evidence": "<what this machine ran of it>", "reason": "after-deploy"`,
	} {
		if !strings.Contains(review, want) {
			t.Errorf("the review prompt does not say %q", want)
		}
	}
}
