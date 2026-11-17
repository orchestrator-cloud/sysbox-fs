//
// Copyright 2026 Nestybox, Inc.
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

package fuse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/sysio"
)

// A container with the ids the root node's Attr asks for.
type kernelTestContainer struct {
	domain.ContainerIface
	id string
}

func (c kernelTestContainer) ID() string  { return c.id }
func (c kernelTestContainer) UID() uint32 { return 0 }
func (c kernelTestContainer) GID() uint32 { return 0 }

// The major:minor of the fuse connection mounted at mp, read from mountinfo
// (proc(5)); "" when there is none.
func kernelTestConnDev(t *testing.T, mp string) string {
	t.Helper()

	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	dev := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 && f[4] == mp && strings.Contains(line, " - fuse sysboxfs ") {
			dev = f[2]
		}
	}
	return dev
}

// The 11-15 chain against the kernel. The fuse guard aborts a box's
// connection; Run's exit and the container's exit both unmount; the box then
// starts again under its own id, on a fresh connection.
//
// Needs root, /dev/fuse and fusectl (a node, or a VM); skipped elsewhere.
func TestAnAbortedConnectionRestartsUnderItsID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root, for a FUSE mount")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("needs /dev/fuse")
	}
	if _, err := os.Stat("/sys/fs/fuse/connections"); err != nil {
		t.Skip("needs fusectl at /sys/fs/fuse/connections")
	}

	fss := NewFuseServerService()
	base := t.TempDir()
	if err := fss.Setup(base, nil, sysio.NewIOService(domain.IOOsFileService), nil); err != nil {
		t.Fatal(err)
	}
	cntr := kernelTestContainer{id: "restart-kernel-test"}
	mp := filepath.Join(base, cntr.id)

	if err := fss.CreateFuseServer(cntr, cntr); err != nil {
		t.Fatalf("first CreateFuseServer() = %v", err)
	}
	dev := kernelTestConnDev(t, mp)
	if dev == "" {
		t.Fatalf("no sysboxfs mount at %s after the first create", mp)
	}
	t.Cleanup(func() {
		fss.DestroyFuseServer(cntr.id)
	})

	// What the guard does: abort the connection, by its minor.
	minor := dev[strings.Index(dev, ":")+1:]
	abort := filepath.Join("/sys/fs/fuse/connections", minor, "abort")
	if err := os.WriteFile(abort, []byte("1"), 0200); err != nil {
		t.Fatalf("aborting connection %s: %v", dev, err)
	}

	// Run's exit unmounts the aborted connection.
	deadline := time.Now().Add(10 * time.Second)
	for kernelTestConnDev(t, mp) == dev {
		if time.Now().After(deadline) {
			t.Fatalf("connection %s still mounted at %s 10 s after its abort", dev, mp)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The container's exit.
	if err := fss.DestroyFuseServer(cntr.id); err != nil {
		t.Fatalf("DestroyFuseServer() = %v", err)
	}

	// The next start, under the same id.
	if err := fss.CreateFuseServer(cntr, cntr); err != nil {
		t.Fatalf("CreateFuseServer() for the same id after the abort = %v: the box is locked out", err)
	}
	again := kernelTestConnDev(t, mp)
	if again == "" || again == dev {
		t.Fatalf("after the restart the mount at %s is on connection %q, want a fresh one (the aborted one was %s)", mp, again, dev)
	}

	if err := fss.DestroyFuseServer(cntr.id); err != nil {
		t.Fatalf("final DestroyFuseServer() = %v", err)
	}
	if d := kernelTestConnDev(t, mp); d != "" {
		t.Errorf("connection %s still mounted at %s after the final destroy", d, mp)
	}
}
