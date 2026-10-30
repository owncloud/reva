package store

import (
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"
	microstore "go-micro.dev/v4/store"
)

func newTestCreds(t *testing.T) string {
	t.Helper()
	accountKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
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
	return string(creds)
}

func storeOptions(opts ...microstore.Option) *microstore.Options {
	o := &microstore.Options{}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

func TestDefaultNatsOptionsCredentials(t *testing.T) {
	t.Run("username and password without credentials", func(t *testing.T) {
		o := defaultNatsOptions(storeOptions(Authentication("user", "pass")))
		require.Equal(t, "user", o.User)
		require.Equal(t, "pass", o.Password)
		require.Nil(t, o.UserJWT)
	})

	t.Run("empty credentials keep username and password", func(t *testing.T) {
		o := defaultNatsOptions(storeOptions(Authentication("user", "pass"), Credentials("", "")))
		require.Equal(t, "user", o.User)
		require.Nil(t, o.UserJWT)
	})

	t.Run("credentials replace username and password", func(t *testing.T) {
		o := defaultNatsOptions(storeOptions(Authentication("user", "pass"), Credentials("", newTestCreds(t))))
		require.Empty(t, o.User)
		require.Empty(t, o.Password)
		require.NotNil(t, o.UserJWT)
		require.NotNil(t, o.SignatureCB)
		_, err := o.UserJWT()
		require.NoError(t, err)
	})

	t.Run("invalid credentials fail on connect", func(t *testing.T) {
		o := defaultNatsOptions(storeOptions(Credentials("", "not a creds file")))
		require.NotNil(t, o.UserJWT)
		_, err := o.UserJWT()
		require.ErrorContains(t, err, "invalid nats creds")
		_, err = o.SignatureCB(nil)
		require.Error(t, err)
	})
}
