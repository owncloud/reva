package ldap

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRetryOpExhaustionPreservesErrorCode — Bug 1:
// RetryOp currently wraps exhaustion as ErrorNetwork regardless of actual last error.
// Callers that key on result codes (e.g. IsErrorWithCode(..., LDAPResultBusy)) are misled.
func TestRetryOpExhaustionPreservesErrorCode(t *testing.T) {
	p := NewReadPolicy(1, 0, 0)
	var slept []time.Duration
	c := newTestConn(t, p, p, nopSleep(&slept))

	err := c.RetryOp(p, func(_ *ldap.Conn) error {
		return ldap.NewError(ldap.LDAPResultBusy, errors.New("server busy"))
	})

	require.Error(t, err)
	assert.False(t, ldap.IsErrorWithCode(err, ldap.ErrorNetwork),
		"exhaustion must not mask real error code with ErrorNetwork")
	assert.True(t, ldap.IsErrorWithCode(err, ldap.LDAPResultBusy),
		"exhaustion must preserve the last real error code")
}

// TestWritePolicyOpaqueErrorNotRetried — Bug 2:
// ldapErrCode maps any non-*ldap.Error to ErrorNetwork, which is retryable for writes.
// An opaque error from a write op must not trigger a retry.
func TestWritePolicyOpaqueErrorNotRetried(t *testing.T) {
	var calls int32
	p := NewWritePolicy(1, 0, 0)
	var slept []time.Duration
	c := newTestConn(t, p, p, nopSleep(&slept))

	_ = c.RetryOp(c.write, func(_ *ldap.Conn) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("opaque non-ldap error") // not a *ldap.Error
	})

	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"opaque non-*ldap.Error must not be retried on a write op")
}

// TestRetryPolicyBackoffGrowsWhenMaxDelayZero — Bug 3:
// With MaxDelay=0, bo.MaxInterval is set to 0, causing all sleep intervals to oscillate
// at BaseDelay (no exponential growth). Sleeps must grow beyond 2×BaseDelay.
// With MaxInterval=0 bug: all 8 sleeps ≤ 1.5×10ms = 15ms.
// With fix (default MaxInterval=60s): sleep[5] ≥ 60ms×0.5 = 30ms > 20ms = 2×BaseDelay.
func TestRetryPolicyBackoffGrowsWhenMaxDelayZero(t *testing.T) {
	const maxRetries = 8
	const baseDelay = 10 * time.Millisecond
	p := NewReadPolicy(maxRetries, baseDelay, 0) // MaxDelay deliberately unset
	var slept []time.Duration
	c := newTestConn(t, p, p, nopSleep(&slept))

	_ = c.RetryOp(p, func(_ *ldap.Conn) error {
		return ldap.NewError(ldap.LDAPResultServerDown, errors.New("down"))
	})

	require.Len(t, slept, maxRetries, "should sleep maxRetries times before exhaustion")

	maxSleep := slept[0]
	for _, d := range slept[1:] {
		if d > maxSleep {
			maxSleep = d
		}
	}
	// Bug present: maxSleep ≤ 15ms (BaseDelay×1.5, oscillating at InitialInterval)
	// Bug fixed:   maxSleep ≥ 30ms (growth reaches default MaxInterval=60s by retry 5)
	assert.Greater(t, maxSleep, 2*baseDelay,
		"backoff with BaseDelay=%v MaxDelay=0 must grow; got maxSleep=%v", baseDelay, maxSleep)
}
