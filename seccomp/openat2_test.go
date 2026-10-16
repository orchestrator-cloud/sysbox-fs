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
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// What an nsenter agent that died mid-request leaves sysbox-fs with.
var errAgent = errors.New("error receiving fd via SCM_RIGHTS: connection reset by peer")

func Test_openat2Again(t *testing.T) {
	tests := []struct {
		name      string
		attempt   int
		agentErr  error
		injectErr error
		waiting   bool
		want      bool
	}{
		{"the agent failed, the caller waits", 1, errAgent, nil, true, true},
		{"the agent failed again, the caller waits", openat2Attempts - 1, errAgent, nil, true, true},
		{"the agent failed the last attempt", openat2Attempts, errAgent, nil, true, false},
		{"the agent failed, the caller waits no more", 1, errAgent, nil, false, false},
		{"the injection failed: the call was interrupted", 1, nil, unix.ESRCH, true, false},
		{"the injection failed: the notification is gone", 1, nil, unix.ENOENT, true, false},
		{"the injection failed: the caller has no fd left", 1, nil, unix.EMFILE, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := openat2Again(tt.attempt, tt.agentErr, tt.injectErr, tt.waiting); got != tt.want {
				t.Errorf("openat2Again(%d, %v, %v, %v) = %v, want %v",
					tt.attempt, tt.agentErr, tt.injectErr, tt.waiting, got, tt.want)
			}
		})
	}
}

// The caller waits on every try: all of the waits between them stay well
// under a second.
func Test_openat2Backoff(t *testing.T) {
	var total time.Duration
	for attempt := 1; attempt < openat2Attempts; attempt++ {
		if openat2Backoff(attempt) <= 0 {
			t.Errorf("no wait before attempt %d", attempt+1)
		}
		total += openat2Backoff(attempt)
	}
	if total >= time.Second {
		t.Errorf("the waits between %d attempts add up to %v", openat2Attempts, total)
	}
}

func Test_openat2Retried(t *testing.T) {
	answer := &sysResponse{}
	tests := []struct {
		name       string
		agentFails int   // attempts whose agent fails before one answers
		injectErr  error // what each injection says, once the agent opened it
		waiting    bool
		goneAfter  int // the caller waits no more after this many looks (0: as waiting says)
		wantCalls  int
		wantAnswer bool
		wantAgent  bool
		wantInject bool
	}{
		{"an agent that fails once: answered at the second", 1, nil, true, 0, 2, true, false, false},
		{"an agent that fails twice: answered at the third", 2, nil, true, 0, 3, true, false, false},
		{"an agent that always fails: given up at the last", 99, nil, true, 0, openat2Attempts, false, true, false},
		{"an agent that fails, the caller gone: given up at once", 99, nil, false, 0, 1, false, true, false},
		{"an agent that fails, the caller gone during the wait: no other agent", 99, nil, true, 1, 1, false, true, false},
		{"an injection that fails: given up at once", 0, unix.ESRCH, true, 0, 1, false, false, true},
		{"an agent and an injection that answer: one attempt", 0, nil, true, 0, 1, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, retried, looks := 0, 0, 0
			once := func() (*sysResponse, error, error) {
				calls++
				if calls <= tt.agentFails {
					return nil, errAgent, nil
				}
				if tt.injectErr != nil {
					return nil, nil, tt.injectErr
				}
				return answer, nil, nil
			}
			resp, agentErr, injectErr := openat2Retried(once,
				func() bool {
					looks++
					return tt.waiting && (tt.goneAfter == 0 || looks <= tt.goneAfter)
				},
				func(int, error) { retried++ })
			if calls != tt.wantCalls {
				t.Errorf("%d attempts, want %d", calls, tt.wantCalls)
			}
			if retried != calls-1 && !(tt.goneAfter > 0 && retried == calls) {
				t.Errorf("told of %d retries for %d attempts", retried, calls)
			}
			if (resp == answer) != tt.wantAnswer || (agentErr != nil) != tt.wantAgent || (injectErr != nil) != tt.wantInject {
				t.Errorf("got (%v, %v, %v), want answer %v, agent error %v, injection error %v",
					resp, agentErr, injectErr, tt.wantAnswer, tt.wantAgent, tt.wantInject)
			}
		})
	}
}
