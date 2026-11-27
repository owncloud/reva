package jsoncs3_test

// This file is the only part of the benchmarks that depends on the
// constructor and on the fields of jsoncs3.Manager. When a commit changes
// them, change only this file. The scenarios in bench_test.go then stay the
// same for the old commit and for the new commit.

import (
	"time"

	"github.com/owncloud/reva/v2/pkg/events"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3"
	"github.com/owncloud/reva/v2/pkg/storage/utils/metadata"
)

// newBenchManager returns a new manager with an empty cache. A manager is one
// pod.
func newBenchManager(s metadata.Storage, es events.Stream, maxConcurrency int) (*jsoncs3.Manager, error) {
	return jsoncs3.New(s, nil, 0, es, maxConcurrency)
}

// closeBenchManager stops the background work of a manager. Today a manager
// has no background work that it must stop.
func closeBenchManager(*jsoncs3.Manager) {}

// setStaleGracePeriod sets the time that a pod waits before it removes a
// stale share. 0 removes it at once.
func setStaleGracePeriod(m *jsoncs3.Manager, d time.Duration) {
	m.StaleShareGracePeriod = d
}
