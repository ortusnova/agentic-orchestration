package compose

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/home"
)

const composeFile = "../../compose.yaml"

// Scenario: Pinned upstream image. Every image of the compose project not
// built from this repository or the Sortie fork is referenced by digest.
func TestPinnedUpstreamImage(t *testing.T) {
	problems, err := CheckImages(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	refs, _ := Images(composeFile)
	var own []string
	for _, r := range refs {
		if Own(r) {
			own = append(own, strings.TrimPrefix(strings.Split(r, ":")[0], OwnPrefix))
		}
	}
	sort.Strings(own)
	own = slices.Compact(own)
	if want := []string{"control-plane", "sortie", "workbench"}; !slices.Equal(own, want) {
		t.Errorf("the project runs the images %v built here, want %v", own, want)
	}
}

// The images built here start from bases pinned by digest too.
func TestBaseImagesPinned(t *testing.T) {
	files, err := filepath.Glob("../../images/*.Dockerfile")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Dockerfiles: %v", err)
	}
	for _, f := range files {
		bases, err := BaseImages(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(bases) == 0 {
			t.Errorf("%s has no base image", f)
		}
		for _, b := range bases {
			if !Pinned(b) {
				t.Errorf("%s: base %s is not pinned by digest", filepath.Base(f), b)
			}
		}
	}
}

func TestCheckImagesFlagsWhatIsNotPinned(t *testing.T) {
	f := filepath.Join(t.TempDir(), "compose.yaml")
	_ = os.WriteFile(f, []byte(`services:
  a:
    image: localhost/ortus-nova/sortie:${CP_TAG:?}
  b:
    image: localhost/ortus-nova/sortie:latest
  c:
    image: docker.io/pomerium/pomerium:v0.33.6
  d:
    image: docker.io/pomerium/pomerium:v0.33.6@sha256:`+strings.Repeat("a", 64)+`
  e:
    image: docker.io/pomerium/pomerium@sha256:`+strings.Repeat("a", 64)+`
`), 0o600)
	problems, err := CheckImages(f)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(problems, "\n")
	for _, want := range []string{"sortie:latest", "pomerium:v0.33.6:", "pomerium@sha256"} {
		if !strings.Contains(got, want) {
			t.Errorf("no problem for %s in:\n%s", want, got)
		}
	}
	if len(problems) != 3 {
		t.Errorf("problems = %d, want 3:\n%s", len(problems), got)
	}
}

// Part of Scenario: No secret in the definition. Every secret the project
// uses is a podman secret made outside the file (external), each service
// takes its secrets only as podman secrets, and no environment value in
// the file names a secret. The live tests search the file and the images
// for the values themselves.
func TestSecretsAreExternalPodmanSecrets(t *testing.T) {
	secrets, err := Secrets(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cp-github-basic": true}
	for _, s := range home.Secrets {
		want[s.Name] = true
	}
	for name, def := range secrets {
		if def != "{external: true}" {
			t.Errorf("secret %s is %q, want {external: true}", name, def)
		}
		if !want[name] {
			t.Errorf("secret %s is not one cpctl knows", name)
		}
	}
	for name := range want {
		if _, ok := secrets[name]; !ok {
			t.Errorf("secret %s is not declared", name)
		}
	}
	b, _ := os.ReadFile(composeFile)
	envKey := regexp.MustCompile(`^\s{6}([A-Z][A-Z0-9_]*):\s*(.*)$`)
	secretish := regexp.MustCompile(`TOKEN|SECRET|PASS|KEY|CREDENTIAL`)
	for line := range strings.SplitSeq(string(b), "\n") {
		if m := envKey.FindStringSubmatch(line); m != nil && secretish.MatchString(m[1]) {
			t.Errorf("environment %s is set in the file; a secret must be a podman secret", m[1])
		}
		if strings.Contains(line, "file:") {
			t.Errorf("a file secret is a bind mount, not a podman secret: %s", strings.TrimSpace(line))
		}
	}
}
