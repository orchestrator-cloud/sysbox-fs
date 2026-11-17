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

import "testing"

// The connection's identity at mount is the sysboxfs mount on top at its
// path, read from real mountinfo lines: optional fields vary in number, and
// paths are escaped.
func TestConnDevIsTheSysboxfsMountOnTop(t *testing.T) {
	const mountinfo = `21 1 8:1 / / rw,relatime shared:1 - ext4 /dev/sda1 rw,errors=remount-ro
30 21 0:52 / /var/lib/sysboxfs/a\040b rw,nosuid,nodev,relatime shared:40 - fuse sysboxfs rw,user_id=0,group_id=0,default_permissions,allow_other
31 30 0:53 / /var/lib/sysboxfs/a\040b rw,nosuid,nodev,relatime - fuse sysboxfs rw,user_id=0,group_id=0,default_permissions,allow_other
32 21 0:54 / /var/lib/sysboxfs/c rw,relatime shared:41 master:7 - tmpfs tmpfs rw
33 21 0:55 / /var/lib/sysboxfs/d rw,relatime - fuse.other sysboxfs rw
`
	seam := mountinfoFn
	mountinfoFn = func() ([]byte, error) { return []byte(mountinfo), nil }
	t.Cleanup(func() { mountinfoFn = seam })

	for _, tc := range []struct {
		path, dev string
	}{
		{"/var/lib/sysboxfs/a b", "0:53"}, // stacked: the top one
		{"/var/lib/sysboxfs/c", ""},       // not a fuse mount
		{"/var/lib/sysboxfs/d", ""},       // not sysbox-fs's
		{"/var/lib/sysboxfs/e", ""},       // no mount
	} {
		dev, err := connDevAt(tc.path)
		if dev != tc.dev || (err == nil) != (tc.dev != "") {
			t.Errorf("connDevAt(%q) = %q, %v; want %q", tc.path, dev, err, tc.dev)
		}
	}
}
