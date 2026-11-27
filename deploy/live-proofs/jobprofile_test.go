package main

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// jobProfile is one entry of WORKFLOW.md's environment.remote.profiles.
type jobProfile struct {
	image        string
	capabilities []string
}

// workflowProfiles reads environment.remote.profiles from the kit's
// WORKFLOW.md: each profile's image and required capabilities.
func workflowProfiles(t *testing.T) map[string]jobProfile {
	t.Helper()
	b, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	name := regexp.MustCompile(`^      ([a-z0-9-]+):\s*$`)
	image := regexp.MustCompile(`^        image:\s*(\S+)\s*$`)
	caps := regexp.MustCompile(`^        requires:\s*\{.*capabilities:\s*\[([^\]]*)\]`)
	out := map[string]jobProfile{}
	in, current := false, ""
	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case line == "---" && in:
			return out
		case strings.TrimSpace(line) == "profiles:" && strings.HasPrefix(line, "    profiles:"):
			in = true
			continue
		case !in || strings.HasPrefix(strings.TrimSpace(line), "#"):
			continue
		case !strings.HasPrefix(line, "      ") && strings.TrimSpace(line) != "":
			in = false
			continue
		}
		if m := name.FindStringSubmatch(line); m != nil {
			current = m[1]
			out[current] = jobProfile{}
			continue
		}
		p := out[current]
		if m := image.FindStringSubmatch(line); m != nil {
			p.image = m[1]
		}
		if m := caps.FindStringSubmatch(line); m != nil {
			for c := range strings.SplitSeq(m[1], ",") {
				p.capabilities = append(p.capabilities, strings.TrimSpace(c))
			}
		}
		if current != "" {
			out[current] = p
		}
	}
	return out
}

// worker is a fake worker's declaration as the matcher reads it: what it
// offers a job.
type worker struct {
	name         string
	capabilities []string
}

// p1 and taichi as the kit renders their workers (worker-config.sh): both
// run smolvm machines with KVM, and only taichi's declares nested.
var (
	p1     = worker{"p1", []string{"linux", "kvm", "smolvm"}}
	taichi = worker{"taichi", []string{"linux", "kvm", "nested", "smolvm"}}
)

// placer is a fake Sortie matcher over the workflow's job profiles: a fix
// run that names a profile goes to the first worker, p1 first, that offers
// every capability the profile requires. One that names none goes to p1,
// as the stage-0 pack's requirements let it.
func placer(profiles map[string]jobProfile, workers ...worker) func(map[string]string) string {
	return func(fix map[string]string) string {
		var need []string
		if name, ok := fix["profile"]; ok {
			p, declared := profiles[name]
			if !declared {
				return ""
			}
			need = p.capabilities
		}
		for _, w := range workers {
			if !slices.ContainsFunc(need, func(c string) bool { return !slices.Contains(w.capabilities, c) }) {
				return w.name
			}
		}
		return ""
	}
}

// The workflow declares the live-proof profile nested: it requires nested
// and runs in the live-proof pack.
func TestTheWorkflowDeclaresTheNestedProfile(t *testing.T) {
	p, ok := workflowProfiles(t)["nested"]
	if !ok {
		t.Fatalf("WORKFLOW.md declares no nested job profile: %v", workflowProfiles(t))
	}
	if !slices.Contains(p.capabilities, "nested") {
		t.Errorf("the nested profile requires %v, want nested", p.capabilities)
	}
	if p.image != "live-proof.smolmachine" {
		t.Errorf("the nested profile's image = %q, want the live-proof pack", p.image)
	}
}

// Scenario: A live proof that needs nested KVM or smolvm. The supervisor's
// request names the nested profile; the kit starts the job as a fix run
// with that profile, and Sortie places it on taichi, never on p1's worker,
// which declares no nested. The job's message names the profile, so the
// job knows it runs in the live-proof pack.
func TestANestedProofIsNeverPlacedOnP1(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.place = placer(workflowProfiles(t), p1, taichi)
	const smolvmProof = "The worker daemon's nested machine could not run: this machine has no /dev/kvm and no smolvm. " +
		"Criterion: a machine started inside the job machine boots."
	sc.sortie.requestWith(11, smolvmProof, "nested", sc.clock)
	sc.pass()
	if len(sc.sortie.fixes) != 1 {
		t.Fatalf("fixes = %v, want one", sc.sortie.fixes)
	}
	fix := sc.sortie.fixes[0]
	if fix["profile"] != "nested" || !strings.Contains(fix["message"], "\nProfile: nested\n") {
		t.Fatalf("fix = %v, want the live-proof job with the nested profile", fix)
	}
	if !slices.Equal(sc.sortie.placed, []string{"taichi"}) {
		t.Fatalf("placed on %v, want taichi only", sc.sortie.placed)
	}
	rec, err := sc.s.load()
	if err != nil || rec.Proofs[homelabPR] == nil || rec.Proofs[homelabPR].Profile != "nested" {
		t.Fatalf("record = %+v, %v; want the proof's profile kept", rec, err)
	}
	sc.wantStatus(headA, "pending", "the live-proof job runs")

	// With taichi gone, the nested job goes nowhere: never to p1.
	sc2 := newScenario(t)
	sc2.sortie.place = placer(workflowProfiles(t), p1)
	sc2.sortie.requestWith(12, smolvmProof, "nested", sc2.clock)
	sc2.pass()
	if !slices.Equal(sc2.sortie.placed, []string{""}) {
		t.Fatalf("with p1 alone the nested job was placed on %v, want nowhere", sc2.sortie.placed)
	}
}

// A request that names no profile keeps its repository's: the fix run
// names none, and any worker may take a story whose repository needs no
// nested.
func TestARequestWithoutAProfileNamesNone(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.place = placer(workflowProfiles(t), p1, taichi)
	sc.sortie.request(4, staging, sc.clock)
	sc.pass()
	if len(sc.sortie.fixes) != 1 {
		t.Fatalf("fixes = %v, want one", sc.sortie.fixes)
	}
	if _, named := sc.sortie.fixes[0]["profile"]; named {
		t.Errorf("fix = %v, want no profile", sc.sortie.fixes[0])
	}
	if rec, _ := sc.s.load(); rec.Proofs[homelabPR].Profile != "" {
		t.Errorf("record = %+v, want no profile", rec.Proofs[homelabPR])
	}
}

// A parked story's nested proof: Sortie starts the job itself, with the
// profile its request named; the kit asks for no fix run.
func TestAParkedStorysNestedProof(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.parked = []string{"ONS-41"}
	sc.sortie.requestWith(13, "boot a machine in the job machine", "nested", sc.clock)
	sc.pass()
	if len(sc.sortie.fixes) != 0 {
		t.Fatalf("fixes = %v, want none: Sortie runs the job of a parked story", sc.sortie.fixes)
	}
	if rec, _ := sc.s.load(); rec.Proofs[homelabPR].Profile != "nested" {
		t.Errorf("record = %+v, want the nested profile", rec.Proofs[homelabPR])
	}
	sc.wantActive("ONS-41\n")
}
