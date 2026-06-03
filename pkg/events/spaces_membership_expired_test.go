package events_test

import (
	"context"
	"testing"
	"time"

	group "github.com/cs3org/go-cs3apis/cs3/identity/group/v1beta1"
	user "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/events/stream"
)

func TestPublishMembershipExpired_user(t *testing.T) {
	pub := make(chan interface{}, 1)
	s := stream.Chan{pub, make(chan interface{})}

	expiredAtSec := time.Now().Add(-time.Hour).Unix()
	em := events.ExpiredMembership{
		SpaceOwner:   &user.UserId{OpaqueId: "owner1"},
		SpaceID:      &provider.StorageSpaceId{OpaqueId: "space1"},
		SpaceName:    "My Space",
		ExpiredAtSec: expiredAtSec,
		GranteeUser:  &user.UserId{OpaqueId: "u1"},
	}

	err := events.PublishMembershipExpired(context.Background(), s, em)
	require.NoError(t, err)

	got := <-pub
	ev, ok := got.(events.SpaceMembershipExpired)
	require.True(t, ok, "expected SpaceMembershipExpired, got %T", got)
	assert.Equal(t, "space1", ev.SpaceID.OpaqueId)
	assert.Equal(t, "My Space", ev.SpaceName)
	assert.Equal(t, "u1", ev.GranteeUserID.OpaqueId)
	assert.Nil(t, ev.GranteeGroupID)
	assert.Equal(t, time.Unix(expiredAtSec, 0), ev.ExpiredAt)
	assert.NotNil(t, ev.Timestamp)
}

func TestPublishMembershipExpired_group(t *testing.T) {
	pub := make(chan interface{}, 1)
	s := stream.Chan{pub, make(chan interface{})}

	expiredAtSec := time.Now().Add(-time.Minute).Unix()
	em := events.ExpiredMembership{
		SpaceOwner:   &user.UserId{OpaqueId: "owner1"},
		SpaceID:      &provider.StorageSpaceId{OpaqueId: "space1"},
		SpaceName:    "My Space",
		ExpiredAtSec: expiredAtSec,
		GranteeGroup: &group.GroupId{OpaqueId: "g1"},
	}

	err := events.PublishMembershipExpired(context.Background(), s, em)
	require.NoError(t, err)

	got := <-pub
	ev, ok := got.(events.SpaceMembershipExpired)
	require.True(t, ok, "expected SpaceMembershipExpired, got %T", got)
	assert.Equal(t, "g1", ev.GranteeGroupID.OpaqueId)
	assert.Nil(t, ev.GranteeUserID)
}
