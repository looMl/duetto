//go:build windows

// Package power keeps the computer awake while a session is active.
package power

import (
	"runtime"
	"syscall"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

var setThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// KeepAwake prevents system sleep until the returned function is called.
//
// The execution state belongs to the calling OS thread and is dropped if that
// thread exits, while goroutines migrate freely between threads. So the call
// is made from a goroutine locked to its own thread that lives until release.
func KeepAwake() (release func()) {
	stop := make(chan struct{})
	ready := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		_, _, _ = setThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired))
		close(ready)
		<-stop
		_, _, _ = setThreadExecutionState.Call(uintptr(esContinuous))
	}()
	<-ready
	return func() { close(stop) }
}
