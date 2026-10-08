package lifecycle

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processTree struct {
	job    windows.Handle
	closed sync.Once
}

func newProcessTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create a job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("set the job object to kill its processes when it closes: %w", err)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	return &processTree{job: job}, nil
}

func (t *processTree) assign(pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(t.job, process); err != nil {
		return fmt.Errorf("assign process %d to its job object: %w", pid, err)
	}
	return resumeThreads(uint32(pid))
}

func resumeThreads(pid uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("list the threads of process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		if err := resumeThread(entry.ThreadID); err != nil {
			return fmt.Errorf("resume thread %d of process %d: %w", entry.ThreadID, pid, err)
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil
	}
	return fmt.Errorf("list the threads of process %d: %w", pid, err)
}

func resumeThread(id uint32) error {
	thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, id)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(thread) }()
	_, err = windows.ResumeThread(thread)
	return err
}

func (t *processTree) kill() {
	t.closed.Do(func() { _ = windows.CloseHandle(t.job) })
}
