// Command ripen is the entry point for the Ripen binary. Everything it
// does lives in internal/cli, so the same command surface can be driven
// from a test without a process.
package main

import (
	"os"

	"github.com/frankieramirez/ripen/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
