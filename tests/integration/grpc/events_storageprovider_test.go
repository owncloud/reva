// Copyright 2018-2021 CERN
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

package grpc_test

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/metadata"

	userpb "github.com/cs3org/go-cs3apis/cs3/identity/user/v1beta1"
	rpcv1beta1 "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
	storagep "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/owncloud/reva/v2/pkg/auth/scope"
	ctxpkg "github.com/owncloud/reva/v2/pkg/ctx"
	revaevents "github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/events/stream"
	"github.com/owncloud/reva/v2/pkg/rgrpc/todo/pool"
	jwt "github.com/owncloud/reva/v2/pkg/token/manager/jwt"
)

func consumeEvent(ch <-chan revaevents.Event, timeout time.Duration) (revaevents.Event, error) {
	select {
	case ev := <-ch:
		return ev, nil
	case <-time.After(timeout):
		return revaevents.Event{}, fmt.Errorf("timeout waiting for event after %s", timeout)
	}
}

var _ = Describe("ocis storage provider events", func() {
	var (
		revads         map[string]*Revad
		ctx            context.Context
		providerClient storagep.ProviderAPIClient
		spacesClient   storagep.SpacesAPIClient
		eventCh        <-chan revaevents.Event
		stopNats       func()

		user = &userpb.User{
			Id: &userpb.UserId{
				Idp:      "0.0.0.0:19000",
				OpaqueId: "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c",
				Type:     userpb.UserType_USER_TYPE_PRIMARY,
			},
			Username: "einstein",
		}
	)

	JustBeforeEach(func() {
		var err error

		natsAddr, stop, err := startNats()
		Expect(err).ToNot(HaveOccurred())
		stopNats = stop

		revads, err = startRevads([]RevadConfig{
			{Name: "storage", Config: "storageprovider-ocis-events.toml"},
			{Name: "permissions", Config: "permissions-ocis-ci.toml"},
		}, map[string]string{
			"nats_address": natsAddr,
			"enable_home":  "true",
		})
		Expect(err).ToNot(HaveOccurred())

		ctx = context.Background()
		tokenManager, err := jwt.New(map[string]interface{}{"secret": "changemeplease"})
		Expect(err).ToNot(HaveOccurred())
		sc, err := scope.AddOwnerScope(nil)
		Expect(err).ToNot(HaveOccurred())
		t, err := tokenManager.MintToken(ctx, user, sc)
		Expect(err).ToNot(HaveOccurred())
		ctx = ctxpkg.ContextSetToken(ctx, t)
		ctx = metadata.AppendToOutgoingContext(ctx, ctxpkg.TokenHeader, t)
		ctx = ctxpkg.ContextSetUser(ctx, user)

		providerClient, err = pool.GetStorageProviderServiceClient(revads["storage"].GrpcAddress)
		Expect(err).ToNot(HaveOccurred())
		spacesClient, err = pool.GetSpacesProviderServiceClient(revads["storage"].GrpcAddress)
		Expect(err).ToNot(HaveOccurred())

		natsStream, err := stream.NatsFromConfig("events-test", true, stream.NatsConfig{
			Endpoint: natsAddr,
		})
		Expect(err).ToNot(HaveOccurred())
		eventCh, err = revaevents.Consume(natsStream, "integration-test",
			revaevents.ContainerCreated{},
			revaevents.SpaceCreated{},
		)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		for _, r := range revads {
			Expect(r.Cleanup(CurrentSpecReport().Failed())).To(Succeed())
		}
		if stopNats != nil {
			stopNats()
		}
	})

	Context("with a personal space", func() {
		JustBeforeEach(func() {
			res, err := spacesClient.CreateStorageSpace(ctx, &storagep.CreateStorageSpaceRequest{
				Owner: user,
				Type:  "personal",
				Name:  user.Id.OpaqueId,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(res.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))
			_, err = consumeEvent(eventCh, 3*time.Second) // discard SpaceCreated
			Expect(err).ToNot(HaveOccurred())
		})

		It("emits ContainerCreated with correct fields", func() {
			newDirRef := &storagep.Reference{
				ResourceId: &storagep.ResourceId{
					SpaceId:  "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c",
					OpaqueId: "f7fbf8c8-139b-4376-b307-cf0a8c2d0d9c",
				},
				Path: "/newdir",
			}

			res, err := providerClient.CreateContainer(ctx, &storagep.CreateContainerRequest{Ref: newDirRef})
			Expect(err).ToNot(HaveOccurred())
			Expect(res.Status.Code).To(Equal(rpcv1beta1.Code_CODE_OK))

			ev, err := consumeEvent(eventCh, 3*time.Second)
			Expect(err).ToNot(HaveOccurred())

			typed, ok := ev.Event.(revaevents.ContainerCreated)
			Expect(ok).To(BeTrue(), "expected ContainerCreated, got %T", ev.Event)
			Expect(typed.Executant).ToNot(BeNil())
			Expect(typed.Executant.OpaqueId).To(Equal(user.Id.OpaqueId))
			Expect(typed.Ref).ToNot(BeNil())
			Expect(typed.SpaceOwner).ToNot(BeNil())
		})
	})
})
