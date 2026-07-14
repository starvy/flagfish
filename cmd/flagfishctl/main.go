// Command flagfishctl is the challenge-as-code CLI.
//
// It consumes the same public REST API third-party integrators use, so a change
// that breaks this tool breaks every integrator and we find out at our own desk.
//
// This is a stub: the command surface and help are real, the implementations are
// not built yet, so the surface can be settled and reviewed before the code exists.
//
// Two invariants that are the whole point of the tool:
//
//   - `sync` uploads value_hash = sha256(flag), never the plaintext. The plaintext
//     flag lives in the author's repo and never touches the platform.
//   - `sync` only creates and updates. It never deletes a challenge absent from the
//     repo, because deleting a challenge mid-event destroys its solves. Removal is
//     an explicit, confirmed `rm`. Loud beats silent.
package main

import (
	"errors"
	"fmt"
	"os"
)

type command struct {
	name    string
	summary string
	run     func(args []string) error
}

func commands() []command {
	return []command{
		{"new", "scaffold a new challenge directory (TOML frontmatter + Markdown body)", stub("new")},
		{"validate", "check frontmatter, schema, refs, and that the description template renders [--json]", stub("validate")},
		{"test", "validate, plus run the [solve] block and check it recovers the flag [--json]", stub("test")},
		{"sync", "idempotent create/update push of a challenge directory. --dry-run diffs; never deletes.", stub("sync")},
		{"pool", "pool stats — the pre-event exhaustion gauge (issued/size per challenge)", poolCmd},
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flagfishctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	switch args[0] {
	case "-h", "--help", "help":
		usage()
		return nil
	}

	for _, c := range commands() {
		if c.name == args[0] {
			return c.run(args[1:])
		}
	}
	usage()
	return fmt.Errorf("unknown command %q", args[0])
}

func usage() {
	fmt.Fprintln(os.Stderr, "flagfishctl — challenge-as-code for flagfish\n\nCommands:")
	for _, c := range commands() {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(os.Stderr, "\nAll commands speak the public REST API. Set FLAGFISH_HOST and FLAGFISH_TOKEN.")
}

// poolCmd has one subcommand today: `pool stats`.
func poolCmd(args []string) error {
	if len(args) == 0 || args[0] != "stats" {
		return errors.New("usage: flagfishctl pool stats")
	}
	return stub("pool stats")(args[1:])
}

// stub is an honest placeholder: it says what it will do and exits non-zero, so a
// script that shells out to an unbuilt command fails rather than silently succeeding.
func stub(name string) func([]string) error {
	return func([]string) error {
		return fmt.Errorf("%s: not implemented yet (stub)", name)
	}
}
