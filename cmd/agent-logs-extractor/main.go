package main

import (
	"fmt"
	"os"

	"github.com/mattjmcnaughton/agent-logs-extractor/internal/cli"
)

func main() {
	if err := cli.NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "agent-logs-extractor:", err)
		os.Exit(1)
	}
}
