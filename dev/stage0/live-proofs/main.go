package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const usage = `usage:
  stage0-live-proofs sync -dir DIR -sortie URL [-github URL]
      one pass; Sortie's control token in STAGE0_LIVE_PROOFS_SORTIE_TOKEN,
      the kit's GitHub token in STAGE0_LIVE_PROOFS_GITHUB_TOKEN
  stage0-live-proofs profile -base egress-profile.json -extra egress-live-proof.json -out FILE
      write the live-proof egress profile`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "stage0-live-proofs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "sync":
		dir := fs.String("dir", "", "the directory of record.json and active")
		sortie := fs.String("sortie", "", "Sortie's base URL")
		github := fs.String("github", "https://api.github.com", "the GitHub API's base URL")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		s := &Syncer{
			Sortie: strings.TrimSuffix(*sortie, "/"), SortieToken: os.Getenv("STAGE0_LIVE_PROOFS_SORTIE_TOKEN"),
			GitHub: strings.TrimSuffix(*github, "/"), GitHubToken: os.Getenv("STAGE0_LIVE_PROOFS_GITHUB_TOKEN"),
			Dir: *dir, Client: &http.Client{Timeout: 30 * time.Second},
			Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		}
		if s.Dir == "" || s.Sortie == "" || s.SortieToken == "" || s.GitHubToken == "" {
			return fmt.Errorf("%s", usage)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
		defer cancel()
		return s.Pass(ctx)
	case "profile":
		base := fs.String("base", "", "the kit's egress profile")
		extra := fs.String("extra", "", "the hosts the live-proof profile adds")
		out := fs.String("out", "", "the file to write")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *base == "" || *extra == "" || *out == "" {
			return fmt.Errorf("%s", usage)
		}
		b, err := renderProfile(*base, *extra)
		if err != nil {
			return err
		}
		return writeFile(*out, b)
	}
	return fmt.Errorf("%s", usage)
}
