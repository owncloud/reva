package node

import (
	"testing"
	"time"

	typespb "github.com/cs3org/go-cs3apis/cs3/types/v1beta1"

	provider "github.com/cs3org/go-cs3apis/cs3/storage/provider/v1beta1"
)

func TestIsGrantExpired(t *testing.T) {
	past := &typespb.Timestamp{Seconds: uint64(time.Now().Add(-time.Hour).Unix())}
	future := &typespb.Timestamp{Seconds: uint64(time.Now().Add(time.Hour).Unix())}

	tests := []struct {
		name string
		g    *provider.Grant
		want bool
	}{
		{"nil grant must not panic", nil, false},
		{"no expiration", &provider.Grant{}, false},
		{"expired", &provider.Grant{Expiration: past}, true},
		{"not yet expired", &provider.Grant{Expiration: future}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGrantExpired(tt.g); got != tt.want {
				t.Fatalf("isGrantExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
