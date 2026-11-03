package stream_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/owncloud/reva/v2/pkg/events/stream"
	"github.com/stretchr/testify/require"
)

func applyOptions(t *testing.T, options []nats.Option) nats.Options {
	t.Helper()
	o := nats.GetDefaultOptions()
	for _, opt := range options {
		require.NoError(t, opt(&o))
	}
	return o
}

func TestNatsOptions(t *testing.T) {
	t.Run("no settings", func(t *testing.T) {
		options, err := stream.NatsOptions(stream.NatsConfig{Endpoint: "nats://127.0.0.1:4222"})
		require.NoError(t, err)
		require.Empty(t, options)
	})

	t.Run("username and password", func(t *testing.T) {
		options, err := stream.NatsOptions(stream.NatsConfig{AuthUsername: "user", AuthPassword: "pass"})
		require.NoError(t, err)
		o := applyOptions(t, options)
		require.Equal(t, "user", o.User)
		require.Equal(t, "pass", o.Password)
		require.Nil(t, o.UserJWT)
	})

	t.Run("invalid creds", func(t *testing.T) {
		_, err := stream.NatsOptions(stream.NatsConfig{Creds: "not a creds file", AuthUsername: "user"})
		require.ErrorContains(t, err, "invalid nats creds")
	})

	t.Run("tls", func(t *testing.T) {
		options, err := stream.NatsOptions(stream.NatsConfig{EnableTLS: true, TLSInsecure: true})
		require.NoError(t, err)
		o := applyOptions(t, options)
		require.True(t, o.Secure)
		require.NotNil(t, o.TLSConfig)
		require.True(t, o.TLSConfig.InsecureSkipVerify)
	})

	t.Run("missing root CA certificate", func(t *testing.T) {
		_, err := stream.NatsOptions(stream.NatsConfig{EnableTLS: true, TLSRootCACertificate: filepath.Join(t.TempDir(), "missing.pem")})
		require.Error(t, err)
	})

	t.Run("invalid root CA certificate", func(t *testing.T) {
		rootCA := filepath.Join(t.TempDir(), "ca.pem")
		require.NoError(t, os.WriteFile(rootCA, []byte("not a certificate"), 0o600))
		_, err := stream.NatsOptions(stream.NatsConfig{EnableTLS: true, TLSRootCACertificate: rootCA})
		require.Error(t, err)
	})

	t.Run("root CA certificate is ignored without tls", func(t *testing.T) {
		options, err := stream.NatsOptions(stream.NatsConfig{TLSRootCACertificate: filepath.Join(t.TempDir(), "missing.pem")})
		require.NoError(t, err)
		require.Empty(t, options)
	})
}
