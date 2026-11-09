// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build windows

package builtin

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"go.uber.org/zap"
	"golang.org/x/sys/windows"
)

// processTree is the shell and every process it starts. On Windows it is a Job
// Object: a process started by a process in the job is in the job too, so the
// job still reaches a descendant after its parent has exited, which
// "taskkill /T" (a walk of the live parent links) cannot. job is zero when the
// job could not be created or the shell could not be assigned to it; the tree
// is then killed with "taskkill /T" as before.
type processTree struct {
	cmd    *exec.Cmd
	job    windows.Handle
	killed bool
}

// setProcessGroup starts the command in a new process group, suspended, so
// containProcessTree can put it in a Job Object before it runs: a process the
// shell started before the assignment would not be in the job.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

// containProcessTree is called right after cmd.Start. It creates a Job Object
// with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, assigns the still-suspended shell to
// it, and then resumes the shell. If the job cannot be set up the shell is
// resumed anyway and the tree falls back to "taskkill /T" (logged as a
// warning). An error means the shell could not be resumed; it has been killed,
// and the caller must still call cmd.Wait to release it.
func containProcessTree(cmd *exec.Cmd) (*processTree, error) {
	tree := &processTree{cmd: cmd}
	pid := uint32(cmd.Process.Pid) // #nosec G115 -- a Windows PID is a DWORD.

	job, err := newKillOnCloseJob()
	if err == nil {
		err = assignToJob(job, pid)
		if err != nil {
			_ = windows.CloseHandle(job)
		} else {
			tree.job = job
		}
	}
	if err != nil {
		zap.L().Warn("shell_execute: Job Object unavailable; descendants are killed with taskkill /T, which misses a descendant whose parent has exited",
			zap.Int("pid", cmd.Process.Pid), zap.Error(err))
	}

	if err := resumeProcess(pid); err != nil {
		killProcessTree(tree)
		releaseProcessTree(tree)
		return nil, fmt.Errorf("resume suspended shell (pid %d): %w", pid, err)
	}
	return tree, nil
}

// newKillOnCloseJob creates an unnamed, non-inheritable Job Object whose
// processes are killed when its last handle is closed. That limit is what kills
// the tree if this server process dies while a command runs;
// releaseProcessTree clears it on a normal completion.
func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("CreateJobObject: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if err := setJobLimits(job, &info); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func setJobLimits(job windows.Handle, info *windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION) error {
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(info)), uint32(unsafe.Sizeof(*info))); err != nil {
		return fmt.Errorf("SetInformationJobObject: %w", err)
	}
	return nil
}

func assignToJob(job windows.Handle, pid uint32) error {
	// The shell is our unreaped child (os/exec holds its handle), so its PID
	// cannot have been reused.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer func() { _ = windows.CloseHandle(proc) }()
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

// resumeProcess resumes every thread of a process started with
// CREATE_SUSPENDED. os/exec closes the main thread's handle inside
// syscall.StartProcess, so the threads are found with a Toolhelp snapshot. A
// suspended new process has only its main thread.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	var firstErr error
	for err = windows.Thread32First(snap, &entry); err == nil; err = windows.Thread32Next(snap, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if oerr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("OpenThread %d: %w", entry.ThreadID, oerr)
			}
			continue
		}
		_, rerr := windows.ResumeThread(thread)
		_ = windows.CloseHandle(thread)
		if rerr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("ResumeThread %d: %w", entry.ThreadID, rerr)
			}
			continue
		}
		resumed++
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return fmt.Errorf("enumerate threads: %w", err)
	}
	if resumed == 0 {
		if firstErr != nil {
			return firstErr
		}
		return errors.New("no thread found for the process")
	}
	return nil
}

// killProcessTree ends the shell and every process in its job, including
// descendants whose parent has already exited. Without a job it falls back to
// "taskkill /T /F", which walks only live parent links. It then kills the
// shell directly in case neither could run. Errors are ignored: the processes
// may already have exited.
func killProcessTree(tree *processTree) {
	if tree == nil || tree.cmd.Process == nil {
		return
	}
	tree.killed = true
	if tree.job == 0 || windows.TerminateJobObject(tree.job, 1) != nil {
		// #nosec G204 -- fixed program and flags; the only argument is our child's PID.
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(tree.cmd.Process.Pid)).Run()
	}
	_ = tree.cmd.Process.Kill()
}

// rekillProcessGroup terminates the job again, catching a process created in
// it while the first termination was being delivered. It is one system call,
// so repeating it every 25 ms is cheap. Without a job it does nothing:
// re-running taskkill would start a process each time.
func rekillProcessGroup(tree *processTree) {
	if tree == nil || tree.job == 0 {
		return
	}
	_ = windows.TerminateJobObject(tree.job, 1)
}

// releaseProcessTree closes the job handle once Execute is done with the
// command. After a timeout, cancellation or overflow the kill-on-close limit
// is left set, so closing the handle also ends anything that joined the job
// after the last termination. After a normal completion the limit is cleared
// first, so a background process that no longer holds the output pipes keeps
// running, as it does on Unix (a process that still holds them keeps the
// command from completing normally, and is killed by the timeout).
func releaseProcessTree(tree *processTree) {
	if tree == nil || tree.job == 0 {
		return
	}
	if !tree.killed {
		_ = setJobLimits(tree.job, &windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{})
	}
	_ = windows.CloseHandle(tree.job)
	tree.job = 0
}
