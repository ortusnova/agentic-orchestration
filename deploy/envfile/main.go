// Command envfile reads the deploy kit's environment files,
// deploy/environments/<env>.yml, checked against environment.schema.json
// beside them (spec store deploy-environments, design D2). Every script of
// the kit reads an environment through it, never by parsing YAML itself.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

const usage = `usage:
  envfile values [-host HOST] FILE   check FILE and print its values as shell assignments;
                                     with -host, also what HOST runs, refusing a host FILE does not declare
  envfile check DIR                  check every <env>.yml in DIR, and that no two share a port or a local name`

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "envfile:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "values":
		host := fs.String("host", "", "the host whose runs to print")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			return fmt.Errorf("%s", usage)
		}
		env, err := Load(fs.Arg(0))
		if err != nil {
			return err
		}
		out, err := env.Assignments(*host)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, out)
		return err
	case "check":
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			return fmt.Errorf("%s", usage)
		}
		envs, err := CheckDir(fs.Arg(0))
		if err != nil {
			return err
		}
		for _, e := range envs {
			if _, err := fmt.Fprintf(stdout, "%s: ok\n", e.Path); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%s", usage)
}
