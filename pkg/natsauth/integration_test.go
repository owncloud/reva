package natsauth_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"
	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/events/stream"
	"github.com/owncloud/reva/v2/pkg/store"
	"github.com/stretchr/testify/require"
	microstore "go-micro.dev/v4/store"
)

// startOperatorServer starts a NATS server in operator (JWT) mode with JetStream.
// It returns the client URL and the ".creds" content of a user in a JetStream enabled account.
func startOperatorServer(t *testing.T) (string, []byte) {
	t.Helper()

	operatorKP, err := nkeys.CreateOperator()
	require.NoError(t, err)
	operatorPub, err := operatorKP.PublicKey()
	require.NoError(t, err)

	sysKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
	sysPub, err := sysKP.PublicKey()
	require.NoError(t, err)
	sysJWT, err := jwt.NewAccountClaims(sysPub).Encode(operatorKP)
	require.NoError(t, err)

	accountKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
	accountPub, err := accountKP.PublicKey()
	require.NoError(t, err)
	accountClaims := jwt.NewAccountClaims(accountPub)
	accountClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage: -1,
		DiskStorage:   -1,
		Streams:       -1,
		Consumer:      -1,
	}
	accountJWT, err := accountClaims.Encode(operatorKP)
	require.NoError(t, err)

	operatorClaims := jwt.NewOperatorClaims(operatorPub)
	operatorClaims.SystemAccount = sysPub
	operatorJWT, err := operatorClaims.Encode(operatorKP)
	require.NoError(t, err)
	operator, err := jwt.DecodeOperatorClaims(operatorJWT)
	require.NoError(t, err)

	resolver := &server.MemAccResolver{}
	require.NoError(t, resolver.Store(sysPub, sysJWT))
	require.NoError(t, resolver.Store(accountPub, accountJWT))

	s, err := server.NewServer(&server.Options{
		Host:             "127.0.0.1",
		Port:             -1,
		JetStream:        true,
		StoreDir:         t.TempDir(),
		TrustedOperators: []*jwt.OperatorClaims{operator},
		AccountResolver:  resolver,
		SystemAccount:    sysPub,
		NoLog:            true,
		NoSigs:           true,
	})
	require.NoError(t, err)
	go s.Start()
	t.Cleanup(s.Shutdown)
	require.True(t, s.ReadyForConnections(10*time.Second))

	userKP, err := nkeys.CreateUser()
	require.NoError(t, err)
	userPub, err := userKP.PublicKey()
	require.NoError(t, err)
	userJWT, err := jwt.NewUserClaims(userPub).Encode(accountKP)
	require.NoError(t, err)
	seed, err := userKP.Seed()
	require.NoError(t, err)
	creds, err := jwt.FormatUserConfig(userJWT, seed)
	require.NoError(t, err)

	return s.ClientURL(), creds
}

func TestJWTAuthentication(t *testing.T) {
	url, creds := startOperatorServer(t)
	credsFile := writeFile(t, creds)

	cases := []struct {
		name      string
		group     string
		credsFile string
		creds     string
	}{
		{name: "creds file", group: "creds-file", credsFile: credsFile},
		{name: "creds content", group: "creds-content", creds: string(creds)},
	}

	for _, tc := range cases {
		t.Run("events with "+tc.name, func(t *testing.T) {
			s, err := stream.NatsFromConfig("natsauth-test", false, stream.NatsConfig{
				Endpoint:  url,
				Cluster:   "natsauth-test",
				CredsFile: tc.credsFile,
				Creds:     tc.creds,
			})
			require.NoError(t, err)

			ch, err := events.Consume(s, "natsauth-test-"+tc.group, events.SpaceCreated{})
			require.NoError(t, err)
			require.NoError(t, events.Publish(context.Background(), s, events.SpaceCreated{Name: tc.name}))

			select {
			case ev := <-ch:
				require.Equal(t, tc.name, ev.Event.(events.SpaceCreated).Name)
			case <-time.After(10 * time.Second):
				t.Fatal("no event received")
			}
		})

		t.Run("nats-js-kv store with "+tc.name, func(t *testing.T) {
			st := store.Create(
				store.Store(store.TypeNatsJSKV),
				microstore.Nodes(url),
				microstore.Database("natsauth-test"),
				microstore.Table("natsauth-test"),
				store.Authentication("ignored", "ignored"),
				store.Credentials(tc.credsFile, tc.creds),
			)
			t.Cleanup(func() { _ = st.Close() })

			require.NoError(t, st.Write(&microstore.Record{Key: tc.name, Value: []byte("value")}))
			recs, err := st.Read(tc.name)
			require.NoError(t, err)
			require.Len(t, recs, 1)
			require.Equal(t, []byte("value"), recs[0].Value)
		})
	}

	t.Run("events with invalid creds fail at once", func(t *testing.T) {
		_, err := stream.NatsFromConfig("natsauth-test", false, stream.NatsConfig{
			Endpoint: url,
			Creds:    "not a creds file",
		})
		require.ErrorContains(t, err, "invalid nats creds")
	})
}
