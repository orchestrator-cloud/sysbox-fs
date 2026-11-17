//
// Copyright 2019-2020 Nestybox, Inc.
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

package state

import (
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nestybox/sysbox-fs/domain"
	"github.com/nestybox/sysbox-fs/mocks"
	"github.com/nestybox/sysbox-fs/process"
	"github.com/nestybox/sysbox-fs/sysio"
	libpidfd "github.com/nestybox/sysbox-libs/pidfd"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/mock"
	"golang.org/x/sys/unix"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
)

// Sysbox-fs global services for all state's pkg unit-tests.
var ios domain.IOServiceIface
var prs domain.ProcessServiceIface
var nss *mocks.NSenterServiceIface
var fss *mocks.FuseServerServiceIface
var hds *mocks.HandlerServiceIface
var mts *mocks.MountServiceIface

func TestMain(m *testing.M) {

	// Disable log generation during UT.
	logrus.SetOutput(ioutil.Discard)

	//
	// Test-cases common settings.
	//
	ios = sysio.NewIOService(domain.IOMemFileService)
	prs = process.NewProcessService()
	nss = &mocks.NSenterServiceIface{}
	hds = &mocks.HandlerServiceIface{}
	fss = &mocks.FuseServerServiceIface{}
	mts = &mocks.MountServiceIface{}

	prs.Setup(ios)

	// Run test-suite.
	m.Run()
}

func Test_containerStateService_Setup(t *testing.T) {
	type fields struct {
		RWMutex    sync.RWMutex
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
		mts        domain.MountServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
	}

	type args struct {
		fss domain.FuseServerServiceIface
		prs domain.ProcessServiceIface
		ios domain.IOServiceIface
		mts domain.MountServiceIface
	}

	a1 := args{
		fss: fss,
		prs: prs,
		ios: ios,
		mts: mts,
	}

	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{"1", f1, a1},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			css := &containerStateService{
				RWMutex:    tt.fields.RWMutex,
				idTable:    tt.fields.idTable,
				netnsTable: tt.fields.netnsTable,
				fss:        tt.fields.fss,
				prs:        tt.fields.prs,
				ios:        tt.fields.ios,
				mts:        tt.fields.mts,
			}
			css.Setup(tt.args.fss, tt.args.prs, tt.args.ios, tt.args.mts)
		})
	}
}

func Test_containerStateService_ContainerCreate(t *testing.T) {

	type fields struct {
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
		mts        domain.MountServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
		mts:        mts,
	}

	css := &containerStateService{
		idTable:    f1.idTable,
		netnsTable: f1.netnsTable,
		fss:        f1.fss,
		prs:        f1.prs,
		ios:        f1.ios,
		mts:        f1.mts,
	}
	type args struct {
		id            string
		initPid       uint32
		ctime         time.Time
		uidFirst      uint32
		uidSize       uint32
		gidFirst      uint32
		gidSize       uint32
		procRoPaths   []string
		procMaskPaths []string
	}

	// Manually create a container to compare with.
	var c1 = &container{
		id:            "1",
		initPid:       1001,
		ctime:         time.Time{},
		uidFirst:      1,
		uidSize:       65535,
		gidFirst:      1,
		gidSize:       65535,
		procRoPaths:   nil,
		procMaskPaths: nil,
		dataStore:     nil,
		initProc:      nil,
		service:       css,
	}

	tests := []struct {
		name   string
		fields fields
		args   args
		want   domain.ContainerIface
	}{
		//
		// Testcase 1: Compare previously create container with a new one to be
		// built through the container's constructor method. They should fully
		// match.
		//
		{"1", f1, args{
			c1.id,
			c1.initPid,
			c1.ctime,
			c1.uidFirst,
			c1.uidSize,
			c1.gidFirst,
			c1.gidSize,
			nil,
			nil,
		}, c1},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := css.ContainerCreate(
				tt.args.id,
				tt.args.initPid,
				tt.args.ctime,
				tt.args.uidFirst,
				tt.args.uidSize,
				tt.args.gidFirst,
				tt.args.gidSize,
				tt.args.procRoPaths,
				tt.args.procMaskPaths,
				css); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("containerStateService.ContainerCreate() = %v, want %v",
					got, tt.want)
			}
		})
	}
}

func Test_containerStateService_ContainerPreRegister(t *testing.T) {

	type fields struct {
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
		mts        domain.MountServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
		mts:        mts,
	}

	css := &containerStateService{
		idTable:    f1.idTable,
		netnsTable: f1.netnsTable,
		fss:        f1.fss,
		prs:        f1.prs,
		ios:        f1.ios,
		mts:        f1.mts,
	}

	// c1 as CreateFuseServer gets it: its pre-registration in flight.
	var c1 = &container{
		id:             "c1",
		service:        css,
		preRegistering: true,
	}

	var c2 = &container{
		id:      "c2",
		service: css,
	}

	type args struct {
		id  string
		id2 string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		prepare func()
	}{
		{
			//
			// Test-case 1: Pre-register a new container.
			//
			name:    "1",
			fields:  f1,
			args:    args{"c1", "c1"},
			wantErr: false,
			prepare: func() {

				css.FuseServerService().(*mocks.FuseServerServiceIface).On(
					"CreateFuseServer", c1, c1).Return(nil)
			},
		},
		{
			//
			// Test-case 2: Pre-register an already-present container (with
			// matching container ID), whose init runs. Error expected.
			//
			name:    "2",
			fields:  f1,
			args:    args{"c2", "c2"},
			wantErr: true,
			prepare: func() {

				fd, err := libpidfd.Open(os.Getpid(), 0)
				if err != nil {
					t.Fatal(err)
				}
				c2.initPid, c2.initPidFd = uint32(os.Getpid()), fd
				f1.idTable[c2.id] = c2
				css.FuseServerService().(*mocks.FuseServerServiceIface).On(
					"CreateFuseServer", c2, c2).Return(nil)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare()
			}

			if err := css.ContainerPreRegister(tt.args.id, ""); (err != nil) != tt.wantErr {
				t.Errorf("containerStateService.ContainerPreRegister() error = %v, wantErr %v",
					err, tt.wantErr)
			}
		})
	}
}

func Test_containerStateService_ContainerRegister(t *testing.T) {

	type fields struct {
		RWMutex    sync.RWMutex
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
		mts        domain.MountServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
		mts:        mts,
	}

	var c1 = &container{
		id:       "c1",
		initPid:  1001,
		initProc: f1.prs.ProcessCreate(1001, 0, 0),
	}

	var c2 = &container{
		id: "c2",
	}

	var c3 = &container{
		id:       "c3",
		initPid:  3003,
		initProc: f1.prs.ProcessCreate(3003, 0, 0),
	}

	type args struct {
		c domain.ContainerIface
	}

	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		prepare func(css *containerStateService)
	}{
		{
			//
			// Test-case 1: Register a pre-registered container with valid
			// user-ns.
			//
			name:    "1",
			fields:  f1,
			args:    args{c1},
			wantErr: false,
			prepare: func(css *containerStateService) {

				c1.service = css

				c1.InitProc().CreateNsInodes(123456)

				f1.idTable[c1.id] = c1

				c1.service.MountService().(*mocks.MountServiceIface).On(
					"NewMountInfoParser", c1, c1.initProc, true, true, true).Return(nil, nil)

				css.FuseServerService().(*mocks.FuseServerServiceIface).On(
					"FuseServerCntrRegComplete", c1).Return(nil)
			},
		},
		{
			//
			// Test-case 2: Register a non-pre-registered container. Error
			// expected.
			//
			name:    "2",
			fields:  f1,
			args:    args{c2},
			wantErr: true,
			prepare: func(css *containerStateService) {},
		},
		{
			//
			// Test-case 3: Register a pre-registered container with missing
			// user-ns inode (i.e. missing /proc/pid/ns/<namespaces>). Error
			// expected.
			//
			name:    "3",
			fields:  f1,
			args:    args{c3},
			wantErr: true,
			prepare: func(css *containerStateService) {

				c3.service = css
				f1.idTable[c3.id] = c3

				css.MountService().(*mocks.MountServiceIface).On(
					"NewMountInfoParser", c3, c3.initProc, true, true, true).Return(nil, nil)

				css.FuseServerService().(*mocks.FuseServerServiceIface).On(
					"FuseServerCntrRegComplete", c3).Return(nil)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			css := &containerStateService{
				RWMutex:    tt.fields.RWMutex,
				idTable:    tt.fields.idTable,
				netnsTable: tt.fields.netnsTable,
				fss:        tt.fields.fss,
				prs:        tt.fields.prs,
				ios:        tt.fields.ios,
				mts:        tt.fields.mts,
			}

			// Initialize memory-based mock FS.
			css.ios.RemoveAllIOnodes()

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare(css)
			}

			if err := css.ContainerRegister(tt.args.c); (err != nil) != tt.wantErr {
				t.Errorf("containerStateService.ContainerRegister() error = %v, wantErr %v",
					err, tt.wantErr)
			}
		})
	}
}

func Test_containerStateService_ContainerUpdate(t *testing.T) {
	type fields struct {
		RWMutex    sync.RWMutex
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
		mts        domain.MountServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
		mts:        mts,
	}

	var c1 = &container{
		id:       "c1",
		initProc: f1.prs.ProcessCreate(1001, 0, 0),
	}
	f1.idTable[c1.id] = c1

	var c2 = &container{
		id:       "c2",
		initProc: f1.prs.ProcessCreate(2002, 0, 0),
	}

	type args struct {
		c domain.ContainerIface
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		prepare func(css *containerStateService)
	}{
		{
			//
			// Test-case 1: Update a properly registered container.
			//
			name:    "1",
			fields:  f1,
			args:    args{c1},
			wantErr: false,
			prepare: func(css *containerStateService) {

				c1.service = css

				c1.InitProc().CreateNsInodes(123456)
				inode, _ := c1.InitProc().NetNsInode()

				f1.idTable[c1.id] = c1
				f1.netnsTable[inode] = []*container{c1}

				c1.service.MountService().(*mocks.MountServiceIface).On(
					"NewMountInfoParser", c1, c1.initProc, true, true, true).Return(nil, nil)
			},
		},
		{
			//
			// Test-case 2: Update a container whose container-ID is not present
			// in the idTable. Error expected.
			//
			name:    "2",
			fields:  f1,
			args:    args{c2},
			wantErr: true,
			prepare: func(css *containerStateService) {

				c2.service = css

				c2.InitProc().CreateNsInodes(123456)
				inode, _ := c2.InitProc().NetNsInode()

				f1.netnsTable[inode] = []*container{c2}

				c2.service.MountService().(*mocks.MountServiceIface).On(
					"NewMountInfoParser", c2, c2.initProc, true, true, true).Return(nil, nil)
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			css := &containerStateService{
				RWMutex:    tt.fields.RWMutex,
				idTable:    tt.fields.idTable,
				netnsTable: tt.fields.netnsTable,
				fss:        tt.fields.fss,
				prs:        tt.fields.prs,
				ios:        tt.fields.ios,
				mts:        tt.fields.mts,
			}

			// Initialize memory-based mock FS.
			css.ios.RemoveAllIOnodes()

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare(css)
			}

			if err := css.ContainerUpdate(tt.args.c); (err != nil) != tt.wantErr {
				t.Errorf("containerStateService.ContainerUpdate() error = %v, wantErr %v",
					err, tt.wantErr)
			}
		})
	}
}

func Test_containerStateService_ContainerUnregister(t *testing.T) {
	type fields struct {
		RWMutex    sync.RWMutex
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
	}

	var c1 = &container{
		id:       "c1",
		initProc: f1.prs.ProcessCreate(1001, 0, 0),
	}

	var c2 = &container{
		id:       "c2",
		initProc: f1.prs.ProcessCreate(2002, 0, 0),
	}

	type args struct {
		c domain.ContainerIface
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
		prepare func(css *containerStateService)
	}{
		{
			//
			// Test-case 1: Unregister a valid (properly registered) container.
			//
			name:    "1",
			fields:  f1,
			args:    args{c1},
			wantErr: false,
			prepare: func(css *containerStateService) {

				c1.InitProc().CreateNsInodes(123456)
				inode, _ := c1.InitProc().NetNsInode()

				c1.service = css

				f1.idTable[c1.id] = c1
				f1.netnsTable[inode] = []*container{c1}

				css.FuseServerService().(*mocks.FuseServerServiceIface).On(
					"DestroyFuseServer", c1.id).Return(nil)
			},
		},
		{
			//
			// Test-case 2: Unregister a container with an id not present in
			// idTable. Error expected.
			//
			name:    "2",
			fields:  f1,
			args:    args{c2},
			wantErr: true,
			prepare: func(css *containerStateService) {

				c2.initProc.CreateNsInodes(123456)
				inode, _ := c2.InitProc().NetNsInode()

				c2.service = css

				f1.netnsTable[inode] = []*container{c2}
			},
		},
		{
			//
			// Test-case 3: Unregister a container with valid ID but with missing
			// netns. Error not expected (this can happen when a container pre-registers
			// and immediately unregisters due to some error in sysbox-runc).
			//
			name:    "3",
			fields:  f1,
			args:    args{c1},
			wantErr: false,
			prepare: func(css *containerStateService) {

				c1.service = css
				f1.idTable[c1.id] = c1

				// clear the netns map
				for entry := range f1.netnsTable {
					delete(f1.netnsTable, entry)
				}
			},
		},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			css := &containerStateService{
				RWMutex:    tt.fields.RWMutex,
				idTable:    tt.fields.idTable,
				netnsTable: tt.fields.netnsTable,
				fss:        tt.fields.fss,
				prs:        tt.fields.prs,
				ios:        tt.fields.ios,
			}

			// Initialize memory-based mock FS.
			css.ios.RemoveAllIOnodes()

			// Prepare the mocks.
			if tt.prepare != nil {
				tt.prepare(css)
			}

			if err := css.ContainerUnregister(tt.args.c); (err != nil) != tt.wantErr {
				t.Errorf("containerStateService.ContainerUnregister() error = %v, wantErr %v",
					err, tt.wantErr)
			}
		})
	}
}

func Test_containerStateService_ContainerLookupById(t *testing.T) {
	type fields struct {
		RWMutex    sync.RWMutex
		idTable    map[string]*container
		netnsTable map[domain.Inode][]*container
		fss        domain.FuseServerServiceIface
		prs        domain.ProcessServiceIface
		ios        domain.IOServiceIface
	}

	var f1 = fields{
		idTable:    make(map[string]*container),
		netnsTable: make(map[domain.Inode][]*container),
		fss:        fss,
		prs:        prs,
		ios:        ios,
	}

	var c1 = &container{
		id: "c1",
	}
	f1.idTable[c1.id] = c1

	type args struct {
		id string
	}

	tests := []struct {
		name   string
		fields fields
		args   args
		want   domain.ContainerIface
	}{
		// Lookup a valid/existing container.
		{"1", f1, args{"c1"}, c1},

		// Lookup a container with no matching entry in the idTable.
		// Error expected.
		{"2", f1, args{"c2"}, nil},
	}

	//
	// Testcase executions.
	//
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			css := &containerStateService{
				RWMutex:    tt.fields.RWMutex,
				idTable:    tt.fields.idTable,
				netnsTable: tt.fields.netnsTable,
				fss:        tt.fields.fss,
				prs:        tt.fields.prs,
				ios:        tt.fields.ios,
			}

			if got := css.ContainerLookupById(tt.args.id); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("containerStateService.ContainerLookupById() = %v, want %v",
					got, tt.want)
			}
		})
	}
}

// TestContainerStateService_FuseServerLifecycleDoesNotBlock verifies that a slow
// fuse-server creation or destruction for one container does not block the
// container state operations of other containers.
//
// This is a regression test for nestybox/sysbox#1018: the container state lock
// was held while mounting / unmounting the container's FUSE fs (which waits on
// the kernel), so one stalled mount stalled the registration of every other
// container and the container lookup done for every trapped syscall.
func TestContainerStateService_FuseServerLifecycleDoesNotBlock(t *testing.T) {
	fss := &mocks.FuseServerServiceIface{}
	css := NewContainerStateService().(*containerStateService)
	css.Setup(fss, prs, ios, mts)

	// Fuse-server operations for the "slow" container block until released.
	entered := make(chan struct{})
	release := make(chan struct{})
	stall := func(mock.Arguments) {
		entered <- struct{}{}
		<-release
	}
	isSlow := mock.MatchedBy(func(c *container) bool { return c.id == "slow" })
	fss.On("CreateFuseServer", isSlow, mock.Anything).Run(stall).Return(nil)
	fss.On("DestroyFuseServer", "slow").Run(stall).Return(nil)
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Return(nil)
	fss.On("DestroyFuseServer", mock.Anything).Return(nil)

	// Operations on another container must complete while "slow" is stalled.
	otherCntrOps := func(phase string) {
		done := make(chan struct{})
		go func() {
			css.ContainerLookupById("other")
			css.ContainerPreRegister("other", "")
			css.ContainerUnregister(css.ContainerLookupById("other"))
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("container state operations blocked behind the %s of another container's fuse server", phase)
		}
	}

	slowDone := make(chan error)
	go func() { slowDone <- css.ContainerPreRegister("slow", "") }()
	<-entered
	otherCntrOps("creation")
	release <- struct{}{}
	if err := <-slowDone; err != nil {
		t.Fatalf("ContainerPreRegister() error = %v", err)
	}

	go func() { slowDone <- css.ContainerUnregister(css.ContainerLookupById("slow")) }()
	<-entered
	otherCntrOps("destruction")
	release <- struct{}{}
	if err := <-slowDone; err != nil {
		t.Fatalf("ContainerUnregister() error = %v", err)
	}
}

// The container entry pre-registration leaves for id, given an init: what
// ContainerRegister records (the init's pid and pidfd, its net-ns tracked).
func registerInit(t *testing.T, css *containerStateService, id string, pid int) *container {
	t.Helper()

	fd, err := libpidfd.Open(pid, 0)
	if err != nil {
		t.Fatalf("pidfd_open(%d): %v", pid, err)
	}

	css.Lock()
	defer css.Unlock()
	c := css.idTable[id]
	c.initPid, c.initPidFd = uint32(pid), fd
	c.netnsInode = 4026531992
	css.netnsTable[c.netnsInode] = append(css.netnsTable[c.netnsInode], c)
	c.regCompleted = true

	return c
}

// A mock fuse-server service that logs its calls, in order.
func newLoggingFss(events *[]string) *mocks.FuseServerServiceIface {
	fss := &mocks.FuseServerServiceIface{}
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		*events = append(*events, "create "+args.Get(0).(*container).id)
	}).Return(nil)
	fss.On("DestroyFuseServer", mock.Anything).Run(func(args mock.Arguments) {
		*events = append(*events, "destroy "+args.String(0))
	}).Return(nil)
	return fss
}

func newTestCss(fss domain.FuseServerServiceIface) *containerStateService {
	css := NewContainerStateService().(*containerStateService)
	css.Setup(fss, prs, ios, mts)
	return css
}

func checkEvents(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("fuse-server calls: [%s], want [%s]", strings.Join(got, ", "), strings.Join(want, ", "))
	}
}

// F1 (sysbox-fs PR #133): a pre-registration whose fuse server fails leaves
// no entry behind, nor its net-ns tracked, so the next start of the container
// is not refused.
func TestContainerPreRegisterRollsBackWhenItsFuseServerFails(t *testing.T) {
	fss := &mocks.FuseServerServiceIface{}
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Return(errors.New("FuseServer InitWait error")).Once()
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Return(nil)
	css := newTestCss(fss)

	// A pod's net-ns (the mem fs keeps a ns file's inode as its content).
	const netns, netnsInode = "/var/run/netns/rollback-test", 4026532100
	if err := ios.NewIOnode("", netns, 0).WriteFile([]byte("4026532100")); err != nil {
		t.Fatal(err)
	}

	err := css.ContainerPreRegister("c1", netns)
	if grpcStatus.Code(err) != grpcCodes.Internal {
		t.Fatalf("first ContainerPreRegister() = %v, want codes.Internal", err)
	}
	if css.ContainerLookupById("c1") != nil {
		t.Errorf("the failed pre-registration left an entry for c1")
	}
	if cntrs, ok := css.netnsTable[netnsInode]; ok {
		t.Errorf("the failed pre-registration left net-ns %d tracked for %v", netnsInode, cntrs)
	}
	if err := css.ContainerPreRegister("c1", netns); err != nil {
		t.Errorf("second ContainerPreRegister() = %v, want nil", err)
	}
}

// sysbox-runc died between pre-registration and registration: the entry has
// no init, and serves nobody. The next start replaces it.
func TestPreRegisterReplacesAnEntryThatNeverRegistered(t *testing.T) {
	var events []string
	css := newTestCss(newLoggingFss(&events))

	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatal(err)
	}
	old := css.ContainerLookupById("c1")

	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatalf("second ContainerPreRegister() = %v, want nil", err)
	}

	checkEvents(t, events, "create c1", "destroy c1", "create c1")
	if c := css.ContainerLookupById("c1"); c == nil || c == old {
		t.Errorf("the entry for c1 was not replaced")
	}
}

// The container's init has exited (its exit's unregister never came, or
// failed): the entry is replaced, and its net-ns no longer tracked.
func TestPreRegisterReplacesAnEntryWhoseInitExited(t *testing.T) {
	var events []string
	css := newTestCss(newLoggingFss(&events))

	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatal(err)
	}
	old := registerInit(t, css, "c1", sleep.Process.Pid)
	sleep.Process.Kill()
	sleep.Wait()

	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatalf("ContainerPreRegister() after the init exited = %v, want nil", err)
	}
	if _, err := unix.FcntlInt(uintptr(old.initPidFd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("the replaced entry's pidfd %d is still open (fcntl: %v)", old.initPidFd, err)
	}

	checkEvents(t, events, "create c1", "destroy c1", "create c1")
	if c := css.ContainerLookupById("c1"); c == nil || c == old {
		t.Errorf("the entry for c1 was not replaced")
	}
	for inode, cntrs := range css.netnsTable {
		for _, c := range cntrs {
			if c == old {
				t.Errorf("the replaced entry is still tracked in net-ns %d: a container joining that net-ns would share its state", inode)
			}
		}
	}
}

// A running container, or one still pre-registering, keeps its entry: a
// second start of the same id is refused, as before.
func TestPreRegisterRefusesALiveEntry(t *testing.T) {
	t.Run("its init runs", func(t *testing.T) {
		var events []string
		css := newTestCss(newLoggingFss(&events))
		if err := css.ContainerPreRegister("c1", ""); err != nil {
			t.Fatal(err)
		}
		live := registerInit(t, css, "c1", os.Getpid())

		err := css.ContainerPreRegister("c1", "")
		if grpcStatus.Code(err) != grpcCodes.AlreadyExists {
			t.Errorf("ContainerPreRegister() of a running container = %v, want codes.AlreadyExists", err)
		}
		checkEvents(t, events, "create c1")
		if css.ContainerLookupById("c1") != live {
			t.Errorf("the running container's entry was replaced")
		}
	})

	t.Run("its pre-registration is still mounting", func(t *testing.T) {
		fss := &mocks.FuseServerServiceIface{}
		entered, release := make(chan struct{}), make(chan struct{})
		fss.On("CreateFuseServer", mock.Anything, mock.Anything).Run(func(mock.Arguments) {
			entered <- struct{}{}
			<-release
		}).Return(nil).Once()
		css := newTestCss(fss)

		first := make(chan error)
		go func() { first <- css.ContainerPreRegister("c1", "") }()
		<-entered

		err := css.ContainerPreRegister("c1", "")
		if grpcStatus.Code(err) != grpcCodes.AlreadyExists {
			t.Errorf("ContainerPreRegister() during another's = %v, want codes.AlreadyExists", err)
		}
		fss.AssertNotCalled(t, "DestroyFuseServer", mock.Anything)

		close(release)
		if err := <-first; err != nil {
			t.Errorf("the first ContainerPreRegister() = %v, want nil", err)
		}
	})
}

// An entry whose fuse server is already gone (its pre-registration was still
// mounting when the unregister came) unregisters cleanly: runc delete must
// not fail on it.
func TestUnregisterOfAnEntryWithoutAServerSucceeds(t *testing.T) {
	fss := &mocks.FuseServerServiceIface{}
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Return(nil)
	fss.On("DestroyFuseServer", "c1").Return(domain.ErrFuseServerNotPresent)
	css := newTestCss(fss)
	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatal(err)
	}

	if err := css.ContainerUnregister(css.ContainerLookupById("c1")); err != nil {
		t.Errorf("ContainerUnregister() = %v, want nil", err)
	}
	if css.ContainerLookupById("c1") != nil {
		t.Errorf("the entry for c1 is still there")
	}
}

// sysbox-fs .4 kept a server whose destroy failed, with no container entry:
// it serves nobody, and the next start destroys it before mounting its own.
func TestPreRegisterDestroysAnOrphanServerFirst(t *testing.T) {
	var events []string
	orphan := true
	fss := &mocks.FuseServerServiceIface{}
	fss.On("CreateFuseServer", mock.Anything, mock.Anything).Return(func(domain.ContainerIface, domain.ContainerIface) error {
		events = append(events, "create c1")
		if orphan {
			return domain.ErrFuseServerPresent
		}
		return nil
	})
	fss.On("DestroyFuseServer", "c1").Run(func(mock.Arguments) {
		events = append(events, "destroy c1")
		orphan = false
	}).Return(nil)
	css := newTestCss(fss)

	if err := css.ContainerPreRegister("c1", ""); err != nil {
		t.Fatalf("ContainerPreRegister() with an orphan server = %v, want nil", err)
	}
	if orphan {
		t.Errorf("the orphan server was not destroyed")
	}
	if css.ContainerLookupById("c1") == nil {
		t.Errorf("no entry for c1")
	}
}
