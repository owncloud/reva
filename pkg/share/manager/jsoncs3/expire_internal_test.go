// Copyright 2018-2022 CERN
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// In applying this license, CERN does not waive the privileges and immunities
// granted to it by virtue of its status as an Intergovernmental Organization
// or submit itself to any jurisdiction.

package jsoncs3

// Each ShareExpired event can send a notification. Only the caller that
// removed an expired share publishes the event for it.

import (
	"sync/atomic"
	"testing"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/owncloud/reva/v2/pkg/events"
	helpers "github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/testhelpers"
	microevents "go-micro.dev/v4/events"

	. "github.com/onsi/gomega"
)

// expiredEventStream counts the ShareExpired events.
type expiredEventStream struct {
	nopStream
	expired int32
}

func (s *expiredEventStream) Publish(_ string, msg interface{}, _ ...microevents.PublishOption) error {
	if _, ok := msg.(events.ShareExpired); ok {
		atomic.AddInt32(&s.expired, 1)
	}
	return nil
}

func expiredShare(t *testing.T, f *batchFixture) *collaboration.Share {
	g := NewWithT(t)
	f.share(t, ctxOf("admin"), 0, groupGrantee("group1"), expiredAt)
	shares, err := f.m.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid")
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(shares.Shares).To(HaveLen(1))
	for _, s := range shares.Shares {
		return s
	}
	return nil
}

// Break: each caller that finds the expired share publishes an event, also
// when another pod removed the share first.
func TestExpireSharesPublishesOneEventWhenTwoPodsExpireTheSameShare(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	s := expiredShare(t, f)

	podA, podB := &expiredEventStream{}, &expiredEventStream{}
	a, err := New(f.storage, nil, 0, podA, 0)
	g.Expect(err).ToNot(HaveOccurred())
	b, err := New(f.storage, nil, 0, podB, 0)
	g.Expect(err).ToNot(HaveOccurred())
	_, err = b.Cache.ListSpace(ctxOf("admin"), "storageid", "spaceid") // pod B finds the share
	g.Expect(err).ToNot(HaveOccurred())

	a.expireShares(ctxOf("admin"), []*collaboration.Share{s})
	b.expireShares(ctxOf("admin"), []*collaboration.Share{s})

	g.Expect(atomic.LoadInt32(&podA.expired)).To(Equal(int32(1)), "the pod that removed the share")
	g.Expect(atomic.LoadInt32(&podB.expired)).To(Equal(int32(0)), "the share was already gone")
}

// Break: expireShares publishes the event also when it cannot remove the
// share. The users get an expiry notification, but the share stays.
func TestExpireSharesPublishesNoEventWhenTheRemovalFails(t *testing.T) {
	g := NewWithT(t)
	f := newBatchFixture(t)
	s := expiredShare(t, f)

	stream := &expiredEventStream{}
	cs := &helpers.CountingUploadStorage{Storage: f.storage, FailPath: "/storages/storageid/spaceid.json"}
	m, err := New(cs, nil, 0, stream, 0)
	g.Expect(err).ToNot(HaveOccurred())

	m.expireShares(ctxOf("admin"), []*collaboration.Share{s})

	g.Expect(atomic.LoadInt32(&stream.expired)).To(Equal(int32(0)))
}
