//go:build windows

// Package procgroup ties child processes (ffmpeg, cloudflared) to Duetto's
// lifetime: if Duetto exits for any reason, including the console window
// being closed with ✕, Windows kills them too.
package procgroup

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObject          = kernel32.NewProc("CreateJobObjectW")
	setInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
)

// Mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION (amd64/arm64 layout).
type basicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct{ R, W, O, RB, WB, OB uint64 }

type extendedLimit struct {
	Basic                 basicLimit
	IO                    ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// KillChildrenOnExit puts this process in a job object that kills every
// process in it when the last handle closes, i.e. when Duetto exits.
// Children started afterwards inherit the job automatically. The handle is
// deliberately never closed.
func KillChildrenOnExit() error {
	job, _, err := createJobObject.Call(0, 0)
	if job == 0 {
		return fmt.Errorf("CreateJobObject: %w", err)
	}
	info := extendedLimit{Basic: basicLimit{LimitFlags: jobObjectLimitKillOnJobClose}}
	if ok, _, err := setInformationJobObject.Call(job, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}
	self, _ := syscall.GetCurrentProcess()
	if ok, _, err := assignProcessToJobObject.Call(job, uintptr(self)); ok == 0 {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

// Hide stops a child process from flashing a console window.
func Hide(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
