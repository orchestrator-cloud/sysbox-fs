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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/nestybox/sysbox-fs/domain"
)

// A fake fusermount3, first on PATH, that records each call in a file and
// exits with the given status. bazil's Mount and Unmount run fusermount3, so
// the file counts their unmounts too.
func fakeFusermount(t *testing.T, status int) (calls func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\n", log)
	if status != 0 {
		script += "echo \"fusermount3: failed to unmount $3: Invalid argument\" >&2\n"
	}
	script += fmt.Sprintf("exit %d\n", status)
	if err := os.WriteFile(filepath.Join(dir, "fusermount3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return func() []string {
		b, err := os.ReadFile(log)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return strings.Split(s, "\n")
		}
		return nil
	}
}

// One mount in the fake table.
type fakeMount struct {
	id, parent     int
	dev            string // major:minor
	point          string
	fstype, source string
}

// A fake mount table behind the mountinfo and unmount seams. An unmount takes
// the top mount at its path off the table, as the kernel does; the first
// `busy` plain unmounts answer EBUSY instead.
type fakeMounts struct {
	mounts []fakeMount
	busy   int
	calls  []string
	events *[]string // shared with other fakes, to check the order of steps
}

func (f *fakeMounts) mountinfo() ([]byte, error) {
	var b strings.Builder
	b.WriteString("21 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw\n")
	for _, m := range f.mounts {
		point := strings.ReplaceAll(m.point, " ", `\040`)
		fmt.Fprintf(&b, "%d %d %s / %s rw,nosuid,nodev,relatime shared:%d - %s %s rw,user_id=0,group_id=0,default_permissions,allow_other\n",
			m.id, m.parent, m.dev, point, m.id, m.fstype, m.source)
	}
	return []byte(b.String()), nil
}

func (f *fakeMounts) unmount(path string, flags int) error {
	call := fmt.Sprintf("%s %s", path, unmountFlags(flags))
	f.calls = append(f.calls, call)
	if f.events != nil {
		*f.events = append(*f.events, "unmount "+call)
	}

	if flags&unix.MNT_DETACH == 0 && f.busy > 0 {
		f.busy--
		return unix.EBUSY
	}

	// The top mount at the path is the one no other mount there sits on.
	top := -1
	for i, m := range f.mounts {
		if m.point != path {
			continue
		}
		covered := false
		for _, o := range f.mounts {
			if o.point == path && o.parent == m.id {
				covered = true
			}
		}
		if !covered {
			top = i
		}
	}
	if top < 0 {
		return unix.EINVAL
	}
	f.mounts = append(f.mounts[:top], f.mounts[top+1:]...)
	return nil
}

func unmountFlags(flags int) string {
	var names []string
	if flags&unix.MNT_DETACH != 0 {
		names = append(names, "MNT_DETACH")
	}
	if flags&unix.UMOUNT_NOFOLLOW != 0 {
		names = append(names, "UMOUNT_NOFOLLOW")
	}
	if rest := flags &^ (unix.MNT_DETACH | unix.UMOUNT_NOFOLLOW); rest != 0 {
		names = append(names, fmt.Sprintf("%#x", rest))
	}
	return strings.Join(names, "|")
}

// Puts the fake table behind the seams for the test.
func useFakeMounts(t *testing.T, f *fakeMounts) {
	t.Helper()

	mountinfo, unmount := mountinfoFn, unmountFn
	mountinfoFn, unmountFn = f.mountinfo, f.unmount
	t.Cleanup(func() { mountinfoFn, unmountFn = mountinfo, unmount })
}

// A service under a temp dir, holding a server for id whose mountpoint dir
// exists, as CreateFuseServer leaves it.
func newDestroyTestService(t *testing.T, id string) (*FuseServerService, *fuseServer) {
	t.Helper()

	fss := NewFuseServerService()
	fss.mountPoint = t.TempDir()
	mp := filepath.Join(fss.mountPoint, id)
	if err := os.Mkdir(mp, 0700); err != nil {
		t.Fatal(err)
	}
	srv := &fuseServer{path: "/", mountPoint: mp, service: fss}
	fss.serversMap[id] = srv

	return fss, srv
}

func checkForgotten(t *testing.T, fss *FuseServerService, id string) {
	t.Helper()

	if _, ok := fss.serversMap[id]; ok {
		t.Errorf("serversMap still holds the server for %s: every later start of the container is refused", id)
	}
}

// The 11-15 chain. The guard aborted the connection, Run's exit unmounted it,
// and the container's exit then ran Destroy on a path that was no longer a
// mount: fusermount3 failed with EINVAL, Destroy returned that error, and
// DestroyFuseServer kept the server, so every later start was refused. A
// mount that is already gone is a finished unmount.
func TestDestroyForgetsAServerWhoseMountIsGone(t *testing.T) {
	calls := fakeFusermount(t, 1)
	fss, srv := newDestroyTestService(t, "c1")

	if err := fss.DestroyFuseServer("c1"); err != nil {
		t.Errorf("DestroyFuseServer() = %v, want nil (runc delete must not fail)", err)
	}

	checkForgotten(t, fss, "c1")
	if _, err := os.Stat(srv.mountPoint); !os.IsNotExist(err) {
		t.Errorf("the mountpoint dir %s is still there (stat: %v)", srv.mountPoint, err)
	}
	if c := calls(); len(c) != 0 {
		t.Errorf("fusermount3 ran %v: sysbox-fs unmounts with umount2 itself", c)
	}
}

// A busy mount (a process still holds a file in it) is detached: the path is
// free at once and the connection ends with its last user.
func TestDestroyDetachesABusyMountAndForgetsIt(t *testing.T) {
	fss, srv := newDestroyTestService(t, "c1")
	srv.connDev = "0:99"
	f := &fakeMounts{busy: 1, mounts: []fakeMount{
		{id: 90, parent: 21, dev: "0:99", point: srv.mountPoint, fstype: "fuse", source: "sysboxfs"},
	}}
	useFakeMounts(t, f)

	if err := fss.DestroyFuseServer("c1"); err != nil {
		t.Errorf("DestroyFuseServer() = %v, want nil", err)
	}

	want := []string{
		srv.mountPoint + " UMOUNT_NOFOLLOW",
		srv.mountPoint + " MNT_DETACH|UMOUNT_NOFOLLOW",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("unmount calls:\n%s\nwant:\n%s", strings.Join(f.calls, "\n"), strings.Join(want, "\n"))
	}
	if len(f.mounts) != 0 {
		t.Errorf("mounts left at the path: %v", f.mounts)
	}
	checkForgotten(t, fss, "c1")
}

// The path may hold a mount that is not this connection's: another
// connection's, or a placeholder put there by hand. Destroy leaves it alone,
// says so when this connection's mount stays too, and still forgets the
// server.
func TestDestroyNeverUnmountsAnotherConnection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		connDev  string
		mounts   func(mp string) []fakeMount
		leftOurs bool // this connection's mount (or one that may be) stays at the path
	}{
		{"another connection replaced ours", "0:99", func(mp string) []fakeMount {
			return []fakeMount{{id: 91, parent: 21, dev: "0:77", point: mp, fstype: "fuse", source: "sysboxfs"}}
		}, false},
		{"another mount sits on top of ours", "0:99", func(mp string) []fakeMount {
			return []fakeMount{
				{id: 90, parent: 21, dev: "0:99", point: mp, fstype: "fuse", source: "sysboxfs"},
				{id: 92, parent: 90, dev: "0:120", point: mp, fstype: "tmpfs", source: "tmpfs"},
			}
		}, true},
		{"this connection's mount is unknown", "", func(mp string) []fakeMount {
			return []fakeMount{{id: 90, parent: 21, dev: "0:99", point: mp, fstype: "fuse", source: "sysboxfs"}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fss, srv := newDestroyTestService(t, "c1")
			srv.connDev = tc.connDev
			f := &fakeMounts{mounts: tc.mounts(srv.mountPoint)}
			useFakeMounts(t, f)

			fss.DestroyFuseServer("c1")

			if len(f.calls) != 0 {
				t.Errorf("unmount calls %v: the mount on top at the path is not known to be this connection's", f.calls)
			}
			if _, err := srv.unmount(); (err != nil) != tc.leftOurs {
				t.Errorf("Destroy's unmount = %v; it left this connection's mount: %v", err, tc.leftOurs)
			}
			checkForgotten(t, fss, "c1")
		})
	}
}

// A closer that counts its calls, for Run's connection.
type countingCloser struct{ n int }

func (c *countingCloser) Close() error { c.n++; return nil }

// Run's exit (the serve loop ended: the guard aborted the connection) and the
// container's Destroy both unmount; only the first one reaches the kernel. A
// second one could take a later mount off the path: once the first unmount
// freed the connection's minor, a new connection may get it.
func TestRunExitAndDestroyUnmountOnce(t *testing.T) {
	calls := fakeFusermount(t, 0)
	fss, srv := newDestroyTestService(t, "c1")
	srv.connDev = "0:99"
	f := &fakeMounts{mounts: []fakeMount{
		{id: 90, parent: 21, dev: "0:99", point: srv.mountPoint, fstype: "fuse", source: "sysboxfs"},
	}}
	useFakeMounts(t, f)

	conn := &countingCloser{}
	srv.serveEnded(conn, nil)
	f.mounts = append(f.mounts, fakeMount{id: 95, parent: 21, dev: "0:99", point: srv.mountPoint, fstype: "fuse", source: "sysboxfs"})
	if err := fss.DestroyFuseServer("c1"); err != nil {
		t.Errorf("DestroyFuseServer() = %v, want nil", err)
	}

	if n := len(f.calls) + len(calls()); n != 1 {
		t.Errorf("%d unmounts (umount2 %v, fusermount3 %v), want 1", n, f.calls, calls())
	}
	if conn.n != 1 {
		t.Errorf("the connection was closed %d times, want 1", conn.n)
	}
	checkForgotten(t, fss, "c1")
}

// An IO service whose mountpoint nodes log their MkdirAll and whose root
// can't be read, so CreateFuseServer stops before it mounts.
type mkdirLogIOService struct {
	domain.IOServiceIface
	events *[]string
}

type mkdirLogIOnode struct {
	domain.IOnodeIface
	path   string
	events *[]string
}

func (s mkdirLogIOService) NewIOnode(_, path string, _ os.FileMode) domain.IOnodeIface {
	return mkdirLogIOnode{path: path, events: s.events}
}

func (n mkdirLogIOnode) MkdirAll() error {
	*n.events = append(*n.events, "mkdir "+n.path)
	return nil
}

func (n mkdirLogIOnode) Stat() (os.FileInfo, error) {
	return nil, errors.New("stub: no root")
}

type idContainer struct {
	domain.ContainerIface
	id string
}

func (c idContainer) ID() string { return c.id }

// A sysboxfs mount left at the container's path (a server forgotten without
// its unmount) is detached before the new server mounts, so mounts never
// stack; a mount that is not sysbox-fs's is left alone.
func TestCreateDetachesALeftoverMountFirst(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mounts func(mp string) []fakeMount
		want   func(mp string) []string
	}{
		{"two stacked leftovers", func(mp string) []fakeMount {
			return []fakeMount{
				{id: 90, parent: 21, dev: "0:55", point: mp, fstype: "fuse", source: "sysboxfs"},
				{id: 91, parent: 90, dev: "0:56", point: mp, fstype: "fuse", source: "sysboxfs"},
			}
		}, func(mp string) []string {
			return []string{
				"unmount " + mp + " MNT_DETACH|UMOUNT_NOFOLLOW",
				"unmount " + mp + " MNT_DETACH|UMOUNT_NOFOLLOW",
				"mkdir " + mp,
			}
		}},
		{"a placeholder that is not sysbox-fs's", func(mp string) []fakeMount {
			return []fakeMount{{id: 92, parent: 21, dev: "0:120", point: mp, fstype: "tmpfs", source: "tmpfs"}}
		}, func(mp string) []string {
			return []string{"mkdir " + mp}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			fss := NewFuseServerService()
			fss.mountPoint = "/var/lib/sysboxfs"
			fss.ios = mkdirLogIOService{events: &events}
			mp := filepath.Join(fss.mountPoint, "c1")
			f := &fakeMounts{mounts: tc.mounts(mp), events: &events}
			useFakeMounts(t, f)

			fss.CreateFuseServer(idContainer{id: "c1"}, idContainer{id: "c1"})

			want := tc.want(mp)
			if strings.Join(events, "\n") != strings.Join(want, "\n") {
				t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(events, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
