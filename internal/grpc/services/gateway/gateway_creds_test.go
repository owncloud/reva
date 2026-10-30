package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChecksCacheCredentials(t *testing.T) {
	for _, name := range []string{"provider_cache_config", "create_personal_space_cache_config"} {
		_, err := New(map[string]interface{}{
			name: map[string]interface{}{"cache_store": "nats-js-kv", "cache_creds": "not a creds file"},
		}, nil, nil)
		require.ErrorContains(t, err, "invalid nats creds", name)
	}
}

func TestNewIgnoresCredentialsOfCacheWithoutNats(t *testing.T) {
	for _, name := range []string{"provider_cache_config", "create_personal_space_cache_config"} {
		_, err := New(map[string]interface{}{
			name: map[string]interface{}{"cache_store": "memory", "cache_creds": "not a creds file"},
		}, nil, nil)
		if err != nil {
			require.NotContains(t, err.Error(), "invalid nats creds", name)
		}
	}
}
