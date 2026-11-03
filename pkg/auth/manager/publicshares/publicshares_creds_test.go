package publicshares

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChecksStoreCredentials(t *testing.T) {
	_, err := New(map[string]interface{}{"store_type": "nats-js-kv", "store_creds": "not a creds file"})
	require.ErrorContains(t, err, "invalid nats creds")

	_, err = New(map[string]interface{}{"store_type": "nats-js-kv", "store_creds_file": "/does/not/exist.creds"})
	require.ErrorContains(t, err, "cannot read nats creds file")
}

func TestNewIgnoresCredentialsOfStoreWithoutNats(t *testing.T) {
	_, err := New(map[string]interface{}{"store_type": "memory", "store_creds_file": "/does/not/exist.creds"})
	require.NoError(t, err)
}
