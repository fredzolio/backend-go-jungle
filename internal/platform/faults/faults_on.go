//go:build faultinject

// Package faults provides crash points for failure tests. Built with
// -tags faultinject, a point listed in JUNGLE_FAULTS (comma separated) kills the
// process immediately (exit 137, no deferred cleanup), like a SIGKILL at that
// exact line.
package faults

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

var enabled = strings.Split(os.Getenv("JUNGLE_FAULTS"), ",")

// Point crashes the process when name is enabled.
func Point(name string) {
	if slices.Contains(enabled, name) {
		fmt.Fprintf(os.Stderr, "fault injected: %s\n", name)
		os.Exit(137)
	}
}
