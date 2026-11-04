// Package compose reads what the control plane's compose file and image
// builds reference, for the checks that every upstream image is pinned by
// digest and that no secret is written into the definition.
package compose

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// OwnPrefix is where the images built from this repository and the Sortie
// fork live; they are tagged with the commit instead of pinned.
const OwnPrefix = "localhost/ortus-nova/"

var (
	imageLine  = regexp.MustCompile(`^\s*image:\s*(\S+)`)
	fromLine   = regexp.MustCompile(`(?i)^FROM\s+(?:--\S+\s+)*(\S+)`)
	digestRef  = regexp.MustCompile(`^[^@\s]+:[^@\s/]+@sha256:[0-9a-f]{64}$`)
	ownRef     = regexp.MustCompile(`^` + regexp.QuoteMeta(OwnPrefix) + `[a-z0-9-]+:\$\{CP_TAG(:\?[^}]*)?\}$`)
	topKey     = regexp.MustCompile(`^([a-z_-]+):`)
	secretItem = regexp.MustCompile(`^  ([a-z0-9-]+):\s*(.*)$`)
)

// Images returns every image reference of a compose file.
func Images(file string) ([]string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the repository's file
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if m := imageLine.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, m[1])
		}
	}
	return out, sc.Err()
}

// BaseImages returns the image of every FROM line of a Dockerfile that is
// not an earlier stage of the same file.
func BaseImages(file string) ([]string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the repository's file
	if err != nil {
		return nil, err
	}
	stages := map[string]bool{}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		m := fromLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !stages[strings.ToLower(m[1])] {
			out = append(out, m[1])
		}
		if f := strings.Fields(line); len(f) >= 4 && strings.EqualFold(f[len(f)-2], "AS") {
			stages[strings.ToLower(f[len(f)-1])] = true
		}
	}
	return out, sc.Err()
}

// Pinned reports whether ref names its image by digest.
func Pinned(ref string) bool { return digestRef.MatchString(ref) }

// Own reports whether ref is an image this repository builds, tagged with
// the commit.
func Own(ref string) bool { return ownRef.MatchString(ref) }

// CheckImages returns one problem per image of a compose file that is
// neither built here and tagged with the commit nor pinned by digest.
func CheckImages(file string) ([]string, error) {
	refs, err := Images(file)
	if err != nil {
		return nil, err
	}
	var problems []string
	for _, r := range refs {
		switch {
		case strings.HasPrefix(r, OwnPrefix):
			if !Own(r) {
				problems = append(problems, fmt.Sprintf("%s: an image built here must be tagged ${CP_TAG}", r))
			}
		case !Pinned(r):
			problems = append(problems, fmt.Sprintf("%s: an upstream image must be pinned by digest (pin-images.sh)", r))
		}
	}
	return problems, nil
}

// Secrets returns the top-level secrets of a compose file, each with its
// definition as written on its line.
func Secrets(file string) (map[string]string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the repository's file
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	in := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if m := topKey.FindStringSubmatch(line); m != nil {
			in = m[1] == "secrets"
			continue
		}
		if !in {
			continue
		}
		if m := secretItem.FindStringSubmatch(line); m != nil {
			out[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return out, sc.Err()
}

var (
	serviceLine  = regexp.MustCompile(`^  ([a-z0-9-]+):\s*$`)
	secretsLine  = regexp.MustCompile(`^    secrets:\s*$`)
	listItem     = regexp.MustCompile(`^      - (.*)$`)
	secretField  = regexp.MustCompile(`\b(source|type|target):\s*([^,}\s]+)`)
	secretInFile = regexp.MustCompile(`/run/secrets/([A-Za-z0-9_.-]+)`)
)

// Service is what a service of a compose file does with secrets: the
// files under /run/secrets its secrets become, and the /run/secrets
// paths its definition names.
type Service struct {
	SecretFiles map[string]bool
	Mentions    []string
}

// Services reads the secret files and mentions of every service of a
// compose file.
func Services(file string) (map[string]*Service, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the repository's file
	if err != nil {
		return nil, err
	}
	out := map[string]*Service{}
	var cur *Service
	inServices, inSecrets := false, false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		if m := topKey.FindStringSubmatch(line); m != nil {
			inServices, cur = m[1] == "services", nil
			continue
		}
		if !inServices {
			continue
		}
		if m := serviceLine.FindStringSubmatch(line); m != nil {
			cur = &Service{SecretFiles: map[string]bool{}}
			out[m[1]] = cur
			inSecrets = false
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			inSecrets = secretsLine.MatchString(line)
		}
		if inSecrets {
			if m := listItem.FindStringSubmatch(line); m != nil {
				item := strings.TrimSpace(m[1])
				if !strings.HasPrefix(item, "{") {
					cur.SecretFiles[item] = true
					continue
				}
				f := map[string]string{}
				for _, kv := range secretField.FindAllStringSubmatch(item, -1) {
					f[kv[1]] = kv[2]
				}
				if f["type"] == "env" {
					continue
				}
				name := f["source"]
				if f["target"] != "" {
					name = f["target"]
				}
				cur.SecretFiles[name] = true
				continue
			}
		}
		for _, m := range secretInFile.FindAllStringSubmatch(line, -1) {
			cur.Mentions = append(cur.Mentions, m[1])
		}
	}
	return out, sc.Err()
}
