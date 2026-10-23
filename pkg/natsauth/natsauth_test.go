package natsauth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/owncloud/reva/v2/pkg/natsauth"
	"github.com/stretchr/testify/require"
)

// newCreds returns the content of a ".creds" file for a new user.
// If seedOf is not nil, the file contains the seed of seedOf instead of the user seed.
func newCreds(t *testing.T, seedOf nkeys.KeyPair) []byte {
	t.Helper()

	accountKP, err := nkeys.CreateAccount()
	require.NoError(t, err)
	userKP, err := nkeys.CreateUser()
	require.NoError(t, err)
	userPub, err := userKP.PublicKey()
	require.NoError(t, err)
	userJWT, err := jwt.NewUserClaims(userPub).Encode(accountKP)
	require.NoError(t, err)

	if seedOf == nil {
		seedOf = userKP
	}
	seed, err := seedOf.Seed()
	require.NoError(t, err)
	creds, err := jwt.FormatUserConfig(userJWT, seed)
	require.NoError(t, err)
	return creds
}

func writeFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "user.creds")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

// requireJWTOption checks that opt sets the JWT callbacks on nats.Options.
func requireJWTOption(t *testing.T, opt nats.Option) {
	t.Helper()
	require.NotNil(t, opt)
	o := nats.GetDefaultOptions()
	require.NoError(t, opt(&o))
	require.NotNil(t, o.UserJWT)
	require.NotNil(t, o.SignatureCB)
	_, err := o.UserJWT()
	require.NoError(t, err)
	_, err = o.SignatureCB([]byte("nonce"))
	require.NoError(t, err)
}

func TestCredentialsOption(t *testing.T) {
	creds := newCreds(t, nil)

	t.Run("no credentials", func(t *testing.T) {
		opt, err := natsauth.CredentialsOption("", "")
		require.NoError(t, err)
		require.Nil(t, opt)
	})

	t.Run("creds content", func(t *testing.T) {
		opt, err := natsauth.CredentialsOption("", string(creds))
		require.NoError(t, err)
		requireJWTOption(t, opt)
	})

	t.Run("creds file", func(t *testing.T) {
		path := writeFile(t, creds)
		opt, err := natsauth.CredentialsOption(path, "")
		require.NoError(t, err)
		requireJWTOption(t, opt)
	})

	t.Run("creds content takes precedence over creds file", func(t *testing.T) {
		opt, err := natsauth.CredentialsOption("/does/not/exist.creds", string(creds))
		require.NoError(t, err)
		requireJWTOption(t, opt)
	})

	t.Run("missing creds file", func(t *testing.T) {
		_, err := natsauth.CredentialsOption(filepath.Join(t.TempDir(), "missing.creds"), "")
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("invalid creds content", func(t *testing.T) {
		_, err := natsauth.CredentialsOption("", "not a creds file")
		require.Error(t, err)
	})

	t.Run("invalid creds file", func(t *testing.T) {
		_, err := natsauth.CredentialsOption(writeFile(t, []byte("not a creds file")), "")
		require.Error(t, err)
	})
}

func TestValidate(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		require.NoError(t, natsauth.Validate(newCreds(t, nil)))
	})

	t.Run("seed of another user", func(t *testing.T) {
		otherKP, err := nkeys.CreateUser()
		require.NoError(t, err)
		require.ErrorContains(t, natsauth.Validate(newCreds(t, otherKP)), "does not match")
	})

	t.Run("account seed instead of user seed", func(t *testing.T) {
		accountKP, err := nkeys.CreateAccount()
		require.NoError(t, err)
		accountSeed, err := accountKP.Seed()
		require.NoError(t, err)
		lines := strings.Split(string(newCreds(t, nil)), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "SU") {
				lines[i] = string(accountSeed)
			}
		}
		require.ErrorIs(t, natsauth.Validate([]byte(strings.Join(lines, "\n"))), nkeys.ErrInvalidUserSeed)
	})

	t.Run("jwt without seed", func(t *testing.T) {
		creds := string(newCreds(t, nil))
		jwtOnly := creds[:strings.Index(creds, "************************* IMPORTANT")]
		require.Error(t, natsauth.Validate([]byte(jwtOnly)))
	})

	t.Run("error does not contain the seed", func(t *testing.T) {
		otherKP, err := nkeys.CreateUser()
		require.NoError(t, err)
		seed, err := otherKP.Seed()
		require.NoError(t, err)
		err = natsauth.Validate(newCreds(t, otherKP))
		require.Error(t, err)
		require.NotContains(t, err.Error(), string(seed))
	})
}
