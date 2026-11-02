//go:build !windows

// Package procgroup ties child processes to Duetto's lifetime (Windows only).
package procgroup

import "os/exec"

// KillChildrenOnExit is a no-op outside Windows (exec.CommandContext plus
// graceful shutdown already stop children there).
func KillChildrenOnExit() error { return nil }

// Hide is a no-op outside Windows.
func Hide(*exec.Cmd) {}
