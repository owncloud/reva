package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewChecksCacheCredentials(t *testing.T) {
	for _, name := range []string{"provider_cache_config", "create_personal_space_cache_config"} {
		_, err := New(map[string]interface{}{
			name: map[string]interface{}{"cache_creds": "not a creds file"},
		}, nil, nil)
		require.ErrorContains(t, err, "invalid nats creds", name)
	}
}
