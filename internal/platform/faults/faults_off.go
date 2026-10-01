//go:build !faultinject

// Package faults provides crash points for failure tests. In production builds
// (without the faultinject tag) every point compiles to a no-op.
package faults

// Point is a no-op in production builds.
func Point(string) {}
