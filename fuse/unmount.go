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

// The unmount of a fuse server's mount. sysbox-fs is root and unconfined, so
// it calls umount2(2) itself rather than bazil's fusermount3 -u: no setuid
// helper, no AppArmor profile, no utab step, and no child for sysbox-fs's
// zombie reaper to take before the unmount can wait for it (sysbox#1044). It
// only ever unmounts its own connection's mount, known by the major:minor
// mountinfo gives that mount.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// Seams for the tests: the mount table, and the unmount call.
var (
	mountinfoFn = func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }
	unmountFn   = unix.Unmount
)

// One mount, from a line of /proc/self/mountinfo (proc(5)).
type mountEntry struct {
	id, parent int
	dev        string // major:minor; a fuse mount's minor is its connection's number in /sys/fs/fuse/connections
	point      string
	fstype     string
	source     string
}

// A mount of sysbox-fs's: a "fuse" mount named "sysboxfs" (bfuse.FSName in
// Run). The fuse guard knows them by the same two fields.
func (m mountEntry) sysboxfs() bool {
	return m.fstype == "fuse" && m.source == "sysboxfs"
}

func parseMountinfo(b []byte) []mountEntry {
	var mounts []mountEntry

	for _, line := range strings.Split(string(b), "\n") {
		// id parent major:minor root point options [optional...] - fstype source superoptions
		f := strings.Fields(line)
		sep := -1
		for i := 6; i < len(f); i++ {
			if f[i] == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+2 >= len(f) {
			continue
		}
		id, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		parent, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		mounts = append(mounts, mountEntry{
			id:     id,
			parent: parent,
			dev:    f[2],
			point:  unescapeMountinfo(f[4]),
			fstype: f[sep+1],
			source: unescapeMountinfo(f[sep+2]),
		})
	}

	return mounts
}

// mountinfo writes space, tab, newline and backslash as \ooo (octal).
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}

	return b.String()
}

// The mounts at path, and the top one: the one no other mount at the path
// sits on (a mount over a mountpoint has the mount below as its parent).
func mountsAt(path string) ([]mountEntry, *mountEntry, error) {
	b, err := mountinfoFn()
	if err != nil {
		return nil, nil, fmt.Errorf("reading mountinfo: %w", err)
	}

	var at []mountEntry
	for _, m := range parseMountinfo(b) {
		if m.point == path {
			at = append(at, m)
		}
	}

	var top *mountEntry
	for i := range at {
		covered := false
		for _, o := range at {
			if o.parent == at[i].id {
				covered = true
				break
			}
		}
		if !covered {
			top = &at[i]
		}
	}

	return at, top, nil
}

// The connection just mounted at path: the sysboxfs mount on top there. Read
// from mountinfo, as a stat of the mount's root would ask the server itself,
// which is not serving yet.
func connDevAt(path string) (string, error) {
	_, top, err := mountsAt(path)
	if err != nil {
		return "", err
	}
	if top == nil || !top.sysboxfs() {
		return "", fmt.Errorf("no sysboxfs mount on top at %s", path)
	}

	return top.dev, nil
}

// unmountConn takes connection dev's mount off path. It returns what it found
// and did, for the logs. It never unmounts a mount that is not dev's: with dev
// unknown (""), or another mount on top of dev's, it leaves the path as it is.
func unmountConn(path, dev string) (string, error) {
	for try := 0; try < 3; try++ {
		at, top, err := mountsAt(path)
		if err != nil {
			return "", err
		}

		ours, others := false, false
		for _, m := range at {
			if m.sysboxfs() && dev != "" && m.dev == dev {
				ours = true
			} else if m.sysboxfs() {
				others = true
			}
		}
		if !ours {
			if dev == "" && others {
				return "", fmt.Errorf("this connection's mount is unknown; left the sysboxfs mount at %s", path)
			}
			if top != nil {
				logrus.Warnf("Leaving the %s mount %s at %s: it is not this connection's (%s)",
					top.fstype, top.dev, path, dev)
			}
			return "not mounted", nil
		}
		if top == nil || top.dev != dev {
			return "", fmt.Errorf("another mount is on top of this connection's (%s) at %s; left both", dev, path)
		}

		done := "unmounted"
		err = unmountFn(path, unix.UMOUNT_NOFOLLOW)
		if errors.Is(err, unix.EBUSY) {
			// Still in use: detach it. The path is free at once; the
			// connection ends when its last user goes.
			logrus.Warnf("FUSE mount %s at %s is busy; detaching it", dev, path)
			done = "detached (busy)"
			err = unmountFn(path, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW)
		}
		if err == nil {
			return done, nil
		}
		if !errors.Is(err, unix.EINVAL) {
			return "", fmt.Errorf("unmounting %s: %w", path, err)
		}
		// EINVAL: no mount at the path any more; read the table again.
	}

	return "", fmt.Errorf("unmounting %s: umount2 answers EINVAL, mountinfo shows connection %s there", path, dev)
}

// detachLeftovers detaches the sysboxfs mounts left at path by a server that
// was forgotten without its unmount, top first, so that a new mount never
// stacks on an old one. A mount on top that is not sysbox-fs's stays, with
// what is under it.
func detachLeftovers(path string) error {
	for n := 0; n < 16; n++ {
		_, top, err := mountsAt(path)
		if err != nil {
			return err
		}
		if top == nil {
			return nil
		}
		if !top.sysboxfs() {
			logrus.Warnf("A %s mount (%s) is at %s; leaving it, and mounting over it",
				top.fstype, top.dev, path)
			return nil
		}

		logrus.Warnf("Detaching the leftover sysboxfs mount %s at %s", top.dev, path)
		err = unmountFn(path, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW)
		if err != nil && !errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("detaching the leftover sysboxfs mount at %s: %w", path, err)
		}
	}

	return fmt.Errorf("sysboxfs mounts still at %s after 16 detaches", path)
}
