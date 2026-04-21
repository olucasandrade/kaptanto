// Command kaptanto is the binary entry point for the kaptanto CDC tool.
// It delegates immediately to the cobra CLI defined in internal/cmd.
package main

import (
	"fmt"
	"os"

	"github.com/olucasandrade/kaptanto/internal/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
