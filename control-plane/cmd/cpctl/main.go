// cpctl is the control plane's operator tool: it keeps the CA, issues
// and revokes worker certificates, and renders Pomerium's configuration
// from the worker list. control-plane/cp.sh applies what it renders.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/home"
	"github.com/ortusnova/agentic-orchestration/control-plane/internal/workers"
)

const usage = `usage: cpctl [-home DIR] COMMAND

  init -public-url https://HOST[:PORT] [-name NAME]... [-address ADDR -sortie URL -egress URL]
                         make the CA, Pomerium's certificate, the egress CA and
                         the generated secrets that are missing; render Pomerium.
                         -address, -sortie and -egress put Pomerium elsewhere than
                         in the compose project, such as the stage-0 kit on p1
  worker issue NAME [-flows control,stdio,...]
                         issue NAME a client certificate and list it
  worker revoke NAME     take NAME off the worker list
  worker list            the listed workers
  render                 render Pomerium's configuration from the worker list
  secrets [-missing]     each secret file, and whether it is there

DIR is CP_HOME, else ~/.local/share/ortus-nova/control-plane. After issue,
revoke or render, apply with control-plane/cp.sh apply.
`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cpctl:", err)
		os.Exit(1)
	}
}

type names []string

func (n *names) String() string     { return strings.Join(*n, ",") }
func (n *names) Set(v string) error { *n = append(*n, v); return nil }

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cpctl", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	dir := fs.String("home", "", "the control-plane directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	h, err := home.Default()
	if err != nil {
		return err
	}
	if *dir != "" {
		h = home.Home{Dir: *dir}
	}
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return errors.New("no command")
	}
	switch args[0] {
	case "init":
		return initCmd(h, args[1:], out)
	case "worker":
		if len(args) < 2 {
			return errors.New("worker: want issue, revoke or list")
		}
		switch args[1] {
		case "issue":
			return issueCmd(h, args[2:], out)
		case "revoke":
			if len(args) != 3 {
				return errors.New("usage: cpctl worker revoke NAME")
			}
			if err := h.RevokeWorker(args[2]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "%s is off the worker list; apply it: control-plane/cp.sh apply\n", args[2])
			return err
		case "list":
			return listCmd(h, out)
		}
		return fmt.Errorf("worker %s: want issue, revoke or list", args[1])
	case "render":
		if err := h.Render(); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, h.PomeriumConfig())
		return err
	case "secrets":
		return secretsCmd(h, args[1:], out)
	}
	fs.Usage()
	return fmt.Errorf("unknown command %q", args[0])
}

func initCmd(h home.Home, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	public := fs.String("public-url", "", "the https URL worker hosts dial, Pomerium's")
	var extra names
	fs.Var(&extra, "name", "another name for Pomerium's certificate (repeatable)")
	var up home.Upstreams
	fs.StringVar(&up.Address, "address", "", "where Pomerium listens, [host]:port; empty: the compose project's")
	fs.StringVar(&up.Sortie, "sortie", "", "Sortie's base URL, http://host:port; empty: the compose project's")
	fs.StringVar(&up.Egress, "egress", "", "the egress gate's base URL, http://host:port; empty: the compose project's")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *public == "" {
		return errors.New("init: -public-url is required")
	}
	made, err := h.Init(*public, extra, up)
	if err != nil {
		return err
	}
	for _, m := range made {
		if _, err := fmt.Fprintln(out, "made", m); err != nil {
			return err
		}
	}
	for _, s := range h.MissingSecrets() {
		if _, err := fmt.Fprintf(out, "missing %s: %s\n", h.Secret(s.Name), s.What); err != nil {
			return err
		}
	}
	return nil
}

func issueCmd(h home.Home, args []string, out io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: cpctl worker issue NAME [-flows control,stdio,...]")
	}
	name := args[0]
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	flows := fs.String("flows", strings.Join(workers.Flows, ","), "the routes the worker may reach")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	is, err := h.IssueWorker(name, strings.Split(*flows, ","))
	if err != nil {
		return err
	}
	s, err := h.LoadSettings()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, `issued %s, fingerprint %s, until %s
copy to the worker host, then delete the key here:
  %s
  %s
  %s
the worker daemon's configuration:
  "worker_id": %q,
  "control_plane": {"url": %q, "tls": {"cert_file": ".../worker.crt", "key_file": ".../worker.key", "ca_file": ".../ca.crt"}}
apply it: control-plane/cp.sh apply
`, name, is.Worker.Fingerprint, is.Worker.NotAfter.Format(time.DateOnly), is.Cert, is.Key, is.CAFile, name, s.PublicURL)
	return err
}

func listCmd(h home.Home, out io.Writer) error {
	l, err := workers.Load(h.WorkerList())
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tUNTIL\tFLOWS\tFINGERPRINT")
	for _, w := range l.Workers {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", w.Name, w.NotAfter.Format(time.DateOnly), strings.Join(w.Flows, ","), w.Fingerprint)
	}
	return tw.Flush()
}

func secretsCmd(h home.Home, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("secrets", flag.ContinueOnError)
	missingOnly := fs.Bool("missing", false, "list only the missing ones, and fail when there are any")
	if err := fs.Parse(args); err != nil {
		return err
	}
	missing := map[string]bool{}
	for _, s := range h.MissingSecrets() {
		missing[s.Name] = true
	}
	for _, s := range home.Secrets {
		if *missingOnly && !missing[s.Name] {
			continue
		}
		state := "present"
		if missing[s.Name] {
			state = "missing"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", s.Name, state, h.Secret(s.Name), s.What); err != nil {
			return err
		}
	}
	if *missingOnly && len(missing) > 0 {
		return fmt.Errorf("%d secret files are missing", len(missing))
	}
	return nil
}
