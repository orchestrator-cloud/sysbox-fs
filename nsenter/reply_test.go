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

package nsenter

import (
	"os"
	"syscall"
	"testing"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/fuse"
	"github.com/nestybox/sysbox-runc/libcontainer/utils"
	"golang.org/x/sys/unix"
)

// The fds this process holds.
func openFds(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("reading /proc/self/fd: %v", err)
	}
	return len(entries)
}

// An agent's reply to an openat2 request, as it sends it: the open it made
// (<opened>, an fd of this process's, -1 for none) and, for a refusal, why
// (<refused>), through the pair sysbox-fs makes, and sysbox-fs's reading of
// it. Returns what sysbox-fs read.
func openat2Reply(t *testing.T, opened int, refused error) (*NSenterEvent, error) {
	t.Helper()
	parent, child, err := utils.NewSockPair("nsenterPipe")
	if err != nil {
		t.Fatalf("socket pair: %v", err)
	}
	defer parent.Close()
	defer child.Close()

	agent := &NSenterEvent{fileDescr: openat2ReplyFds(opened)}
	if refused != nil {
		agent.ResMsg = &domain.NSenterMessage{
			Type:    domain.ErrorResponse,
			Payload: &fuse.IOerror{RcvError: refused},
		}
	} else {
		agent.ResMsg = &domain.NSenterMessage{Type: domain.Openat2SyscallResponse}
	}
	if err := agent.sendReply(child); err != nil {
		t.Fatalf("the agent's reply: %v", err)
	}
	read := &NSenterEvent{}
	return read, read.processResponse(parent)
}

// An open the file system refused comes back as that refusal, with its own
// errno and no fd: never as the agent's failure, which sysbox-fs tries again.
func TestOpenat2RefusalIsAnErrorReply(t *testing.T) {
	for _, errno := range []syscall.Errno{unix.ENOENT, unix.EACCES, unix.ENOTDIR} {
		read, err := openat2Reply(t, -1, errno)
		if err != nil {
			t.Fatalf("%v: sysbox-fs read the refusal as the agent's failure: %v", errno, err)
		}
		if read.ResMsg.Type != domain.ErrorResponse {
			t.Fatalf("%v: read a %v, want an error reply", errno, read.ResMsg.Type)
		}
		if code := read.ResMsg.Payload.(fuse.IOerror).Code; code != errno {
			t.Errorf("read errno %v, want %v", code, errno)
		}
	}
}

// What an agent's reply hands over is sysbox-fs's to keep only when it is an
// open's answer: an error reply leaves no fd behind.
func TestOpenat2ReplyLeavesNoFdBehind(t *testing.T) {
	before := openFds(t)
	for i := 0; i < 20; i++ {
		if _, err := openat2Reply(t, -1, unix.ENOENT); err != nil {
			t.Fatalf("a refusal: %v", err)
		}
	}
	if after := openFds(t); after != before {
		t.Errorf("%d fds after 20 refusals, %d before", after, before)
	}

	// an error reply that came with an fd anyway (an agent from before this
	// one sent its stdin): sysbox-fs closes it
	devnull, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	before = openFds(t)
	for i := 0; i < 20; i++ {
		if _, err := openat2Reply(t, int(devnull.Fd()), unix.ENOENT); err != nil {
			t.Fatalf("a refusal with an fd: %v", err)
		}
	}
	if after := openFds(t); after != before {
		t.Errorf("%d fds after 20 refusals that came with an fd, %d before", after, before)
	}

	f, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before = openFds(t)
	read, err := openat2Reply(t, int(f.Fd()), nil)
	if err != nil {
		t.Fatalf("an open's answer: %v", err)
	}
	if read.ResMsg.Type != domain.Openat2SyscallResponse {
		t.Fatalf("read a %v, want an openat2 response", read.ResMsg.Type)
	}
	fd := read.ResMsg.Payload.(domain.Openat2RespPayload).Fd
	if after := openFds(t); after != before+1 {
		t.Errorf("%d fds after an open's answer, want the one it hands on: %d", after, before+1)
	}
	unix.Close(fd)
}
