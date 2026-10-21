// Command deedflow is the DeedFlow server and its management commands.
package main

import (
	"fmt"
	"os"

	"github.com/shahinshabab/deedflow-go/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
