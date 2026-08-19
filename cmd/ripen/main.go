// Command ripen is the entry point for the Ripen binary.
//
// Only the version verb exists yet; the full verb set and the versioned
// Response envelope land with the CLI PR of the rework migration plan
// (docs/rework/SPEC.md).
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/frankieramirez/ripen/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "ripen %s\n", version.String())
		return 0
	default:
		fmt.Fprintf(stderr, "ripen: unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ripen <command>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	fmt.Fprintln(w, "  version    print build metadata")
}
