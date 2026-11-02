//go:build !windows

// Package power keeps the computer awake while a session is active.
package power

// KeepAwake is a no-op outside Windows.
func KeepAwake() (release func()) { return func() {} }
