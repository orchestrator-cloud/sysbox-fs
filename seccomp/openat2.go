//
// Copyright 2019-2025 Nestybox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package seccomp

import (
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/fuse"
	"github.com/nestybox/sysbox-fs/mount"
	libseccomp "github.com/seccomp/libseccomp-golang"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// seccompNotifAddfd matches struct seccomp_notif_addfd from linux/seccomp.h
type seccompNotifAddfd struct {
	id          uint64
	flags       uint32
	srcfd       uint32
	newfd       uint32
	newfd_flags uint32
}

// RESOLVE_* flags for openat2 (from linux/openat2.h)
const (
	RESOLVE_NO_XDEV       = 0x01
	RESOLVE_NO_MAGICLINKS = 0x02
	RESOLVE_NO_SYMLINKS   = 0x04
	RESOLVE_BENEATH       = 0x08
	RESOLVE_IN_ROOT       = 0x10
	RESOLVE_CACHED        = 0x20
)

// openat2Attempts bounds the opens processOpenat2 makes for one openat2 of a
// file under a sysbox-fs mount. Each goes through an nsenter agent -- a
// process sysbox-fs starts, which enters the caller's namespaces, opens the
// file and hands its fd back over a socket -- and on a loaded host that agent
// now and then fails for reasons of its own (it dies, its socket resets, a
// message it was to send never comes): sysbox-fs is one daemon for every
// container on its host. Each such failure answered the caller EINVAL for a
// call it made right: runc's writes of a container's sysctls, docker's
// net.ipv4.ip_unprivileged_port_start among them, failed its start with
// "unsafe procfs detected: openat2 /proc/./sys/...: invalid argument". The
// open is tried again instead, a few times, while the caller still waits on
// it; opening a file under /proc/sys changes nothing, so a second try is the
// first's answer.
const openat2Attempts = 3

// openat2Backoff is how long processOpenat2 waits before another try at
// <attempt> + 1.
func openat2Backoff(attempt int) time.Duration {
	return time.Duration(attempt) * 50 * time.Millisecond
}

// openat2Again says whether processOpenat2 tries the open once more after
// attempt <attempt> failed in the nsenter agent (<agentErr>), or in the
// injection of the fd it opened into the caller (<injectErr>), and the
// caller's notification is still one it waits on (<waiting>). An open the
// agent made and the file system refused (EACCES, ENOENT, ...) is the
// caller's answer, and never comes here. An injection is not tried again: the
// kernel refuses one only when the caller's call was interrupted or it is
// gone (ESRCH, ENOENT), which the kernel then restarts or nobody waits on, or
// for what a second try does not change (EMFILE, EBADF).
func openat2Again(attempt int, agentErr, injectErr error, waiting bool) bool {
	if attempt >= openat2Attempts || !waiting || injectErr != nil {
		return false
	}
	return agentErr != nil
}

// openat2Retried runs <once> -- an open through an nsenter agent and the
// injection of its fd -- until it answers the caller, or openat2Again says to
// stop, <waiting> saying whether the caller's notification is still one it
// waits on: the answer, or what failed last, the agent (<agentErr>) or the
// injection (<injectErr>). <retrying> is told each attempt that failed before
// the next. Args: once, waiting, retrying
func openat2Retried(
	once func() (*sysResponse, error, error),
	waiting func() bool,
	retrying func(attempt int, agentErr error)) (*sysResponse, error, error) {

	for attempt := 1; ; attempt++ {
		resp, agentErr, injectErr := once()
		if resp != nil {
			return resp, nil, nil
		}
		if !openat2Again(attempt, agentErr, injectErr, waiting()) {
			return nil, agentErr, injectErr
		}
		retrying(attempt, agentErr)
		time.Sleep(openat2Backoff(attempt))
	}
}

// openat2SyscallInfo holds the parsed arguments for openat2 syscall
type openat2SyscallInfo struct {
	syscallCtx           // syscall generic info
	dirfd      int32     // directory file descriptor
	path       string    // pathname to open
	flags      uint64    // open_how.flags
	mode       uint64    // open_how.mode
	resolve    uint64    // open_how.resolve
	notifyFd   int32     // seccomp notification file descriptor
	caps       [2]uint32 // effective capabilities of the process
}

// injectFd injects a file descriptor into the traced process using SECCOMP_IOCTL_NOTIF_ADDFD
// Returns the file descriptor number in the target process, or an error
func (si *openat2SyscallInfo) injectFd(srcfd int) (int, error) {

	addfd := seccompNotifAddfd{
		id:          si.reqId,
		flags:       0, // Let the kernel choose the fd number
		srcfd:       uint32(srcfd),
		newfd:       0, // 0 means kernel assigns lowest available fd
		newfd_flags: unix.O_CLOEXEC,
	}

	// Perform the SECCOMP_IOCTL_NOTIF_ADDFD ioctl
	targetFd, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(si.notifyFd),
		uintptr(unix.SECCOMP_IOCTL_NOTIF_ADDFD),
		uintptr(unsafe.Pointer(&addfd)),
	)

	if errno != 0 {
		return -1, errno
	}

	return int(targetFd), nil
}

// processOpenat2 handles the openat2 syscall processing
func (si *openat2SyscallInfo) processOpenat2() (*sysResponse, error) {
	var err error

	t := si.tracer
	fullPath := si.path

	// Per openat2(2): If path is absolute, then dirfd is ignored
	// (unless how.resolve contains RESOLVE_IN_ROOT, in which case
	// path is resolved relative to dirfd).
	isAbsolute := filepath.IsAbs(fullPath)
	resolveInRoot := (si.resolve & RESOLVE_IN_ROOT) != 0

	if !isAbsolute || (isAbsolute && resolveInRoot) {
		// Path needs to be resolved relative to dirfd or cwd
		if si.dirfd == unix.AT_FDCWD {
			// Path is relative to current working directory
			if !isAbsolute {
				fullPath = filepath.Join(si.cwd, fullPath)
			}
			// If absolute with RESOLVE_IN_ROOT, cwd is treated as root,
			// so absolute path is used as-is relative to cwd
		} else {
			// Path is relative to dirfd (or treated as relative if RESOLVE_IN_ROOT is set)
			dirPath, err := si.processInfo.GetFd(si.dirfd)
			if err != nil {
				// If we can't resolve dirfd, just continue with the syscall
				logrus.Debugf("openat2: pid: %d: failed to resolve dirfd %d: %v", si.pid, si.dirfd, err)
				return t.createContinueResponse(si.reqId), nil
			}

			if resolveInRoot && isAbsolute {
				// With RESOLVE_IN_ROOT, absolute paths are resolved relative to dirfd
				// by treating dirfd as the root. So "/foo/bar" becomes "dirfd/foo/bar"
				fullPath = filepath.Join(dirPath, strings.TrimPrefix(fullPath, "/"))
			} else {
				// Normal relative path resolution
				fullPath = filepath.Join(dirPath, fullPath)
			}
		}
	}

	fullPath = filepath.Clean(fullPath)

	fullPath, err = si.processInfo.ResolveProcSelf(fullPath)
	if err != nil {
		logrus.Debugf("openat2: pid: %d: failed to resolve /proc/self in path %s: %v", si.pid, si.path, err)
	}

	// Check if the path is under a sysbox-fs mount
	if mount.IsSysboxfsMount(fullPath) {
		logrus.Infof("openat2(): pid: %d: path under sysbox-fs mount detected: path = %s, flags = %#x, mode = %#x, resolve = %#x",
			si.pid, fullPath, si.flags, si.mode, si.resolve)

		// Drop some of the RESOLVE_* flags. These flags restrict path resolution in ways that don't work due
		// to sysbox-fs emulating some resources under /proc/sys/.
		cleanResolve := si.resolve &^ (RESOLVE_NO_XDEV | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH)
		cleanFlags := si.flags &^ unix.O_PATH

		// Open the file inside the container's namespaces via nsenter.
		payload := domain.Openat2SyscallPayload{
			Header: domain.NSenterMsgHeader{
				Root:         si.root,
				Cwd:          si.cwd,
				Capabilities: si.caps,
			},
			Path:             fullPath,
			Flags:            cleanFlags,
			Mode:             si.mode,
			Resolve:          cleanResolve,
			CheckForSysboxfs: true, // ensure the openat2 is opening a sysbox-fs managed file
		}

		resp, agentErr, _ := openat2Retried(
			func() (*sysResponse, error, error) { return si.openat2Once(payload, fullPath) },
			func() bool {
				return libseccomp.NotifIDValid(libseccomp.ScmpFd(si.notifyFd), si.reqId) == nil
			},
			func(attempt int, err error) {
				logrus.Warnf("openat2(): pid %d: the nsenter agent failed to open %s (attempt %d of %d): %v; trying again",
					si.pid, fullPath, attempt, openat2Attempts, err)
			})
		if resp != nil {
			return resp, nil
		}
		// an agent's failure is processSyscall's EINVAL, as an injection's is here
		if agentErr != nil {
			return nil, agentErr
		}
		return t.createErrorResponse(si.reqId, syscall.EINVAL), nil
	}

	// Otherwise let the kernel handle it
	return t.createContinueResponse(si.reqId), nil
}

// openat2Once opens <fullPath> in the caller's namespaces through an nsenter
// agent and injects the fd it opened into the caller. It answers the caller
// (a response), or says what failed: the agent (<agentErr>), or the
// injection (<injectErr>).
func (si *openat2SyscallInfo) openat2Once(
	payload domain.Openat2SyscallPayload,
	fullPath string) (*sysResponse, error, error) {

	t := si.tracer
	nss := t.service.nss
	event := nss.NewEvent(
		si.pid,
		si.uid,
		si.gid,
		&domain.AllNSs,
		0,
		&domain.NSenterMessage{
			Type:    domain.Openat2SyscallRequest,
			Payload: payload,
		},
		nil,
		false,
	)

	// Launch nsenter-event
	if err := nss.SendRequestEvent(event); err != nil {
		return nil, err, nil
	}

	// Obtain nsenter-event response
	responseMsg := nss.ReceiveResponseEvent(event)
	if responseMsg.Type == domain.ErrorResponse {
		resp := t.createErrorResponse(
			si.reqId,
			responseMsg.Payload.(fuse.IOerror).Code)
		return resp, nil, nil
	}

	// Extract the opened file descriptor from the response (passed to sysbox-fs via SCM_RIGHTS by the nsenter process)
	respPayload := responseMsg.Payload.(domain.Openat2RespPayload)
	fd := respPayload.Fd

	logrus.Infof("openat2(): pid %d: received fd %d from nsenter for path %s", si.pid, fd, fullPath)

	// Inject the file descriptor into the traced process using SECCOMP_IOCTL_NOTIF_ADDFD
	targetFd, err := si.injectFd(fd)

	// Close the fd in sysbox-fs after injecting it (the target process now has a copy)
	unix.Close(fd)

	if err != nil {
		logrus.Errorf("openat2(): pid %d: failed to inject fd %d into target process %d: %v", si.pid, fd, si.pid, err)
		return nil, nil, err
	}

	logrus.Infof("openat2(): pid %d: injected fd %d as %d for path %s", si.pid, fd, targetFd, fullPath)

	// Return success response with the target fd as the return value
	return t.createSuccessResponseWithRetValue(si.reqId, uint64(targetFd)), nil, nil
}
