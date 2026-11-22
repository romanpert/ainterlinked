// Command ainterlinked is the same program as interlinked under the project's
// name, so `go install github.com/romanpert/ainterlinked/cmd/ainterlinked@latest`
// gives the command people see in the docs. Both names work everywhere.
package main

import (
	"os"

	"github.com/romanpert/ainterlinked/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
