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
	"context"
	"os"
	"path/filepath"
	"testing"

	"bazil.org/fuse"
	bfusefs "bazil.org/fuse/fs"

	"github.com/nestybox/sysbox-fs/domain"
)

// Stubs: Open and Create reach only these methods. Each embeds its interface,
// so any other call panics (the mocks package is older than domain's
// interfaces).
type stubContainer struct{ domain.ContainerIface }

type stubIOnode struct{ domain.IOnodeIface }

func (stubIOnode) SetOpenFlags(int)        {}
func (stubIOnode) SetOpenMode(os.FileMode) {}

type stubIOService struct{ domain.IOServiceIface }

func (stubIOService) NewIOnode(string, string, os.FileMode) domain.IOnodeIface {
	return stubIOnode{}
}

type stubHandler struct {
	domain.HandlerIface
	nonSeekable bool
	info        os.FileInfo
}

func (h stubHandler) Open(domain.IOnodeIface, *domain.HandlerRequest) (bool, error) {
	return h.nonSeekable, nil
}

func (h stubHandler) Lookup(domain.IOnodeIface, *domain.HandlerRequest) (os.FileInfo, error) {
	return h.info, nil
}

type stubHandlerService struct {
	domain.HandlerServiceIface
	h stubHandler
}

func (s stubHandlerService) LookupHandler(domain.IOnodeIface) (domain.HandlerIface, bool) {
	return s.h, true
}

// The kernel's FOPEN_NOFLUSH (include/uapi/linux/fuse.h), spelled out here so
// that the test does not lean on the constant it checks.
const kernelFopenNoflush = fuse.OpenResponseFlags(1 << 5)

// A fuse server whose handler opens every node, nonSeekable as given.
func newOpenTestServer(t *testing.T, nonSeekable bool) *fuseServer {
	t.Helper()

	tmp := filepath.Join(t.TempDir(), "max_user_namespaces")
	if err := os.WriteFile(tmp, []byte("0\n"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tmp)
	if err != nil {
		t.Fatal(err)
	}

	hds := stubHandlerService{h: stubHandler{nonSeekable: nonSeekable, info: info}}

	return &fuseServer{
		container: stubContainer{},
		nodeDB:    make(map[string]*bfusefs.Node),
		service:   &FuseServerService{ios: stubIOService{}, hds: hds},
	}
}

func checkOpenFlags(t *testing.T, what string, got fuse.OpenResponseFlags, nonSeekable bool) {
	t.Helper()

	if got&kernelFopenNoflush == 0 {
		t.Errorf("%s: open answer flags %v lack FOPEN_NOFLUSH: each close() would wait in fuse_flush (sysbox#1018)", what, got)
	}
	if got&fuse.OpenDirectIO == 0 {
		t.Errorf("%s: open answer flags %v lack OpenDirectIO", what, got)
	}
	if (got&fuse.OpenNonSeekable != 0) != nonSeekable {
		t.Errorf("%s: open answer flags %v: OpenNonSeekable should be %v", what, got, nonSeekable)
	}
}

func TestOpenNoFlushIsTheKernelBit(t *testing.T) {
	if openNoFlush != kernelFopenNoflush {
		t.Fatalf("openNoFlush is %#x, the kernel's FOPEN_NOFLUSH is %#x", uint32(openNoFlush), uint32(kernelFopenNoflush))
	}
}

func TestFileOpenAsksNoFlush(t *testing.T) {
	for _, nonSeekable := range []bool{false, true} {
		srv := newOpenTestServer(t, nonSeekable)
		f := &File{name: "max_user_namespaces", path: "/proc/sys/user/max_user_namespaces",
			attr: &fuse.Attr{Mode: 0644}, server: srv}

		resp := &fuse.OpenResponse{}
		if _, err := f.Open(context.Background(), &fuse.OpenRequest{}, resp); err != nil {
			t.Fatalf("Open: %v", err)
		}
		checkOpenFlags(t, "File.Open", resp.Flags, nonSeekable)
	}
}

func TestDirCreateAsksNoFlush(t *testing.T) {
	for _, nonSeekable := range []bool{false, true} {
		srv := newOpenTestServer(t, nonSeekable)
		d := &Dir{File: File{name: "user", path: "/proc/sys/user",
			attr: &fuse.Attr{Mode: os.ModeDir | 0755}, server: srv}}

		resp := &fuse.CreateResponse{}
		req := &fuse.CreateRequest{Name: "max_user_namespaces", Mode: 0644}
		if _, _, err := d.Create(context.Background(), req, resp); err != nil {
			t.Fatalf("Create: %v", err)
		}
		checkOpenFlags(t, "Dir.Create", resp.Flags, nonSeekable)
	}
}
