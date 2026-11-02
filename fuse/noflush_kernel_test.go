//
// Copyright 2019-2026 Nestybox, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package fuse

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bazil.org/fuse"
	bfusefs "bazil.org/fuse/fs"

	"github.com/nestybox/sysbox-fs/domain"
)

const kernelTestFileData = "65536\n"

// The stub handler reads as an emulated /proc/sys file does: all of it at
// offset 0, then EOF.
func (h stubHandler) Read(_ domain.IOnodeIface, req *domain.HandlerRequest) (int, error) {
	if req.Offset > 0 {
		return 0, io.EOF
	}
	return copy(req.Data, kernelTestFileData), nil
}

// The kernel half of the sysbox#1018 fix. sysbox-fs's own nodes are served on
// a real FUSE mount, made with the options fuseServer.Run uses, and cat reads
// one emulated file 100 times. Each close() reaches fuse_flush(), which sends
// a FLUSH request and waits for its answer unless the open answer carried
// FOPEN_NOFLUSH: 100 FLUSH requests without the fix, none with it.
//
// Needs root and /dev/fuse (a node, or a VM); skipped elsewhere.
func TestCloseSendsNoFlushToTheKernel(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root, for a FUSE mount")
	}
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("needs /dev/fuse")
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("needs cat")
	}

	srv := newOpenTestServer(t, false)
	srv.root = &Dir{File: File{name: "/", path: "/",
		attr: &fuse.Attr{Mode: os.ModeDir | 0555}, skipIdRemap: true, server: srv}}
	var file bfusefs.Node = &File{name: "max_user_namespaces", path: "/max_user_namespaces",
		attr: &fuse.Attr{Mode: 0444, Nlink: 1}, skipIdRemap: true, server: srv}
	srv.nodeDB["/max_user_namespaces"] = &file

	mnt := t.TempDir()
	c, err := fuse.Mount(mnt, fuse.FSName("sysboxfs"), fuse.AllowOther(), fuse.DefaultPermissions())
	if err != nil {
		t.Fatalf("mount: %v", err)
	}

	var opens, flushes atomic.Int64
	debug := func(msg interface{}) {
		switch s := fmt.Sprint(msg); {
		case strings.HasPrefix(s, "<- Open "):
			opens.Add(1)
		case strings.HasPrefix(s, "<- Flush "):
			flushes.Add(1)
		}
	}
	served := make(chan error, 1)
	go func() { served <- bfusefs.New(c, &bfusefs.Config{Debug: debug}).Serve(srv) }()
	defer func() {
		fuse.Unmount(mnt)
		c.Close()
		<-served
	}()

	path := filepath.Join(mnt, "max_user_namespaces")
	for i := 0; i < 100; i++ {
		out, err := exec.Command(cat, path).Output()
		if err != nil {
			t.Fatalf("cat %s (run %d): %v", path, i+1, err)
		}
		if string(out) != kernelTestFileData {
			t.Fatalf("cat %s (run %d): %q, want %q", path, i+1, out, kernelTestFileData)
		}
	}

	t.Logf("100 cat runs: %d OPEN, %d FLUSH requests", opens.Load(), flushes.Load())
	if opens.Load() < 100 {
		t.Fatalf("%d OPEN requests for 100 opens: the kernel did not ask sysbox-fs each time", opens.Load())
	}
	if n := flushes.Load(); n != 0 {
		t.Fatalf("%d FLUSH requests for 100 closes: each close() waited in fuse_flush (sysbox#1018)", n)
	}
}
