package jsoncs3

import (
	"context"
	"strings"
	"sync"
	"time"

	collaboration "github.com/cs3org/go-cs3apis/cs3/sharing/collaboration/v1beta1"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/owncloud/reva/v2/pkg/appctx"
	"github.com/owncloud/reva/v2/pkg/logger"
	"github.com/owncloud/reva/v2/pkg/share"
	"github.com/owncloud/reva/v2/pkg/share/manager/jsoncs3/shareid"
)

// A listing only filters. It never writes. When it finds a share that it
// cannot use, it notes a suspect in memory. A background loop of the pod works
// through the suspects and changes the documents.
//
// The suspect list is only in memory. A restart loses it. This is acceptable:
// the documents still hold the ghost entry or the expired share, so the next
// listing on any pod finds it again. Until then, the listings filter it.

// defaultCleanupInterval is the default of Manager.CleanupInterval. It decides
// the delay of the ShareExpired event.
const defaultCleanupInterval = time.Minute

// defaultSuspectLimit is the default of Manager.suspectLimit.
const defaultSuspectLimit = 10000

type suspectReason int

const (
	// suspectMissing: a document has the ID, but the space document has no
	// share with this ID.
	suspectMissing suspectReason = iota
	// suspectExpired: the share is expired.
	suspectExpired
)

// suspect is a share that a listing found and could not use.
type suspect struct {
	shareID string
	reason  suspectReason
	// share is the share of an expired suspect. It is read-only. removeShares
	// needs the grantee, the creator and the resource ID.
	share *collaboration.Share
	// users holds the users whose received.json holds the ID.
	users map[string]struct{}
	// groups holds the groups whose document holds the ID.
	groups map[string]struct{}
	// creators holds the users whose created.json holds the ID.
	creators map[string]struct{}
	// first is the time when this pod found the suspect first. last is the time
	// of the last listing that found it.
	first, last time.Time
	// link is the span of the last listing that found the suspect.
	link trace.SpanContext
}

// suspectInput is what a listing knows about a suspect.
type suspectInput struct {
	shareID string
	reason  suspectReason
	share   *collaboration.Share
	user    string
	groups  []string
	creator string
}

// suspectList holds the suspects of one pod.
type suspectList struct {
	mu        sync.Mutex
	byID      map[string]*suspect
	lastSweep time.Time
}

func (m *Manager) now() time.Time {
	if m.staleClock != nil {
		return m.staleClock()
	}
	return time.Now()
}

func (m *Manager) suspectCount() int {
	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	return len(m.suspects.byID)
}

// noteSuspect notes a share that a listing could not use. It does not write.
// When the list is full, it drops a new suspect. A later listing finds it
// again.
func (m *Manager) noteSuspect(ctx context.Context, in suspectInput) {
	now := m.now()
	limit := m.suspectLimit
	if limit <= 0 {
		limit = defaultSuspectLimit
	}
	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	if m.suspects.byID == nil {
		m.suspects.byID = map[string]*suspect{}
	}
	m.sweepSuspects(now)
	s, ok := m.suspects.byID[in.shareID]
	if !ok {
		if len(m.suspects.byID) >= limit {
			return
		}
		s = &suspect{
			shareID:  in.shareID,
			first:    now,
			users:    map[string]struct{}{},
			groups:   map[string]struct{}{},
			creators: map[string]struct{}{},
		}
		m.suspects.byID[in.shareID] = s
	}
	s.last = now
	if in.reason == suspectExpired {
		s.reason = suspectExpired
		s.share = in.share
	}
	if in.user != "" {
		s.users[in.user] = struct{}{}
	}
	for _, g := range in.groups {
		s.groups[g] = struct{}{}
	}
	if in.creator != "" {
		s.creators[in.creator] = struct{}{}
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		s.link = sc
	}
}

// sweepSuspects forgets the suspects that no listing found for
// staleForgetAfter. A sweep reads the full map, so it runs at most once in each
// staleForgetAfter/staleSweepsPerForget. The caller holds the lock.
func (m *Manager) sweepSuspects(now time.Time) {
	forgetAfter := m.staleForgetAfter
	if forgetAfter <= 0 {
		forgetAfter = defaultStaleForgetAfter
	}
	if now.Sub(m.suspects.lastSweep) < forgetAfter/staleSweepsPerForget {
		return
	}
	m.suspects.lastSweep = now
	for id, s := range m.suspects.byID {
		if now.Sub(s.last) > forgetAfter {
			delete(m.suspects.byID, id)
		}
	}
}

// snapshotSuspects returns a copy of the suspects that the loop can handle now.
// An expired suspect has no grace period. A missing suspect waits for the grace
// period, because an older pod writes the grantee entry and the share at the
// same time.
func (m *Manager) snapshotSuspects(now time.Time) (expired, missing []*suspect) {
	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	for _, s := range m.suspects.byID {
		cp := *s
		cp.users = cloneSet(s.users)
		cp.groups = cloneSet(s.groups)
		cp.creators = cloneSet(s.creators)
		switch {
		case s.reason == suspectExpired:
			expired = append(expired, &cp)
		case m.StaleShareGracePeriod <= 0 || now.Sub(s.first) >= m.StaleShareGracePeriod:
			missing = append(missing, &cp)
		}
	}
	return expired, missing
}

func cloneSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k := range in {
		out[k] = struct{}{}
	}
	return out
}

func (m *Manager) dropSuspects(ids ...string) {
	m.suspects.mu.Lock()
	defer m.suspects.mu.Unlock()
	for _, id := range ids {
		delete(m.suspects.byID, id)
	}
}

// CleanupSuspects works through the suspects of this pod once. The background
// loop calls it. It does these steps:
//
//  1. It removes the expired shares and sends the ShareExpired events.
//  2. For each missing share that is past the grace period, it reads the space
//     document again. When the share is there, it drops the suspect. When the
//     share is still missing, it removes the ID from the documents of the
//     users, the groups and the creators.
//
// A failed removal keeps the suspect. The next run tries again.
func (m *Manager) CleanupSuspects(ctx context.Context) {
	expired, missing := m.snapshotSuspects(m.now())
	if len(expired) == 0 && len(missing) == 0 {
		return
	}

	// The run has no request, so it starts a root span. The links lead to the
	// listings that found the suspects.
	var links []trace.Link
	for _, s := range append(append([]*suspect(nil), expired...), missing...) {
		if s.link.IsValid() && len(links) < 32 {
			links = append(links, trace.Link{SpanContext: s.link})
		}
	}
	ctx, span := appctx.GetTracerProvider(ctx).Tracer(tracerName).Start(ctx, "CleanupSuspects",
		trace.WithNewRoot(), trace.WithLinks(links...))
	defer span.End()
	span.SetAttributes(attribute.Int("expired", len(expired)), attribute.Int("missing", len(missing)))

	m.cleanupExpired(ctx, expired)
	m.cleanupMissing(ctx, missing)
	span.SetStatus(codes.Ok, "")
}

func (m *Manager) cleanupExpired(ctx context.Context, expired []*suspect) {
	if len(expired) == 0 {
		return
	}
	logger := appctx.GetLogger(ctx)
	// Read each space again. The suspect holds the share of the listing. Another
	// pod can have renewed or removed the share since. Only a share that is
	// still expired in the document is removed.
	type space struct{ storageID, spaceID string }
	bySpace := map[space][]*suspect{}
	for _, s := range expired {
		storageID, spaceID, _ := shareid.Decode(s.shareID)
		sp := space{storageID, spaceID}
		bySpace[sp] = append(bySpace[sp], s)
	}
	var shares []*collaboration.Share
	var handled []string
	for sp, suspects := range bySpace {
		current, err := m.Cache.ListSpace(ctx, sp.storageID, sp.spaceID)
		if err != nil {
			logger.Warn().Err(err).Str("spaceid", sp.spaceID).Msg("could not read the space to check the expired shares. trying again later")
			continue
		}
		for _, s := range suspects {
			cur := current.Shares[s.shareID]
			if cur != nil && share.IsExpired(cur) {
				shares = append(shares, cur)
			}
			// A share that is gone or renewed needs no removal here. The pod
			// that removed it cleaned the documents.
			handled = append(handled, s.shareID)
		}
	}
	if err := m.expireShares(ctx, shares); err != nil {
		return // keep the suspects, the next run tries again
	}
	m.dropSuspects(handled...)
}

func (m *Manager) cleanupMissing(ctx context.Context, missing []*suspect) {
	if len(missing) == 0 {
		return
	}
	logger := appctx.GetLogger(ctx)

	// Read each space document once.
	type space struct{ storageID, spaceID string }
	bySpace := map[space][]*suspect{}
	for _, s := range missing {
		storageID, spaceID, _ := shareid.Decode(s.shareID)
		sp := space{storageID, spaceID}
		bySpace[sp] = append(bySpace[sp], s)
	}

	type userSpace struct{ userID, ssid string }
	userIDs := map[userSpace][]string{}
	groupIDs := map[string][]string{}
	creatorIDs := map[string][]string{}
	var gone []*suspect
	var present []string
	for sp, suspects := range bySpace {
		spaceShares, err := m.Cache.ListSpace(ctx, sp.storageID, sp.spaceID)
		if err != nil {
			logger.Warn().Err(err).Str("spaceid", sp.spaceID).Msg("could not read the space to check the missing shares. trying again later")
			continue
		}
		// An empty etag means that the download of the space file got NotFound.
		// That can be temporary, so it is no proof that a share is gone. A Stat
		// that also gets NotFound is the proof. A failed first Share in a space
		// leaves a grantee entry without a space file.
		if spaceShares.Etag == "" && !m.Cache.SpaceFileMissing(ctx, sp.storageID, sp.spaceID) {
			logger.Warn().Str("spaceid", sp.spaceID).Msg("the space file is not proven missing. trying again later")
			continue
		}
		ssid := sp.storageID + shareid.IDDelimiter + sp.spaceID
		for _, s := range suspects {
			if spaceShares.Shares[s.shareID] != nil {
				present = append(present, s.shareID) // an older pod wrote the share
				continue
			}
			logger.Warn().Str("shareid", s.shareID).Msg("share not found. cleaning up")
			gone = append(gone, s)
			for u := range s.users {
				k := userSpace{u, ssid}
				userIDs[k] = append(userIDs[k], s.shareID)
			}
			for g := range s.groups {
				groupIDs[g] = append(groupIDs[g], s.shareID)
			}
			for c := range s.creators {
				creatorIDs[c] = append(creatorIDs[c], s.shareID)
			}
		}
	}
	m.dropSuspects(present...)

	fns := make([]func() error, 0, len(userIDs)+len(groupIDs)+len(creatorIDs))
	var failedMu sync.Mutex
	failed := map[string]bool{}
	fail := func(ids []string) {
		failedMu.Lock()
		defer failedMu.Unlock()
		for _, id := range ids {
			failed[id] = true
		}
	}
	for k, ids := range userIDs {
		fns = append(fns, func() error {
			err := m.UserReceivedStates.RemoveMany(ctx, k.userID, k.ssid, ids)
			if err != nil {
				logError(logger, err, "could not remove stale shares from the user received states", ids)
				fail(ids)
			}
			return err
		})
	}
	for g, ids := range groupIDs {
		fns = append(fns, func() error {
			err := m.GroupReceivedCache.RemoveMany(ctx, g, ids)
			if err != nil {
				logError(logger, err, "could not remove stale shares from the group cache", ids)
				fail(ids)
			}
			return err
		})
	}
	for c, ids := range creatorIDs {
		fns = append(fns, func() error {
			err := m.CreatedCache.RemoveMany(ctx, c, ids)
			if err != nil {
				logError(logger, err, "could not remove stale shares from the created shares", ids)
				fail(ids)
			}
			return err
		})
	}
	limitAll(m.removeConcurrency(), fns)
	_ = runAll(fns...)

	var done []string
	for _, s := range gone {
		if !failed[s.shareID] {
			done = append(done, s.shareID)
		}
	}
	m.dropSuspects(done...)
}

func logError(l *zerolog.Logger, err error, msg string, ids []string) {
	l.Error().Err(err).Str("shareids", strings.Join(ids, ",")).Msg(msg)
}

// startCleanupLoop starts the background loop once. A CleanupInterval of zero
// or less switches the loop off.
func (m *Manager) startCleanupLoop() {
	m.loopOnce.Do(func() {
		if m.CleanupInterval <= 0 {
			return
		}
		ctx, cancel := context.WithCancel(logger.New().Level(zerolog.WarnLevel).WithContext(context.Background()))
		m.loopCancel = cancel
		m.loopDone = make(chan struct{})
		go func() {
			defer close(m.loopDone)
			t := time.NewTicker(m.CleanupInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					m.CleanupSuspects(ctx)
				}
			}
		}()
	})
}

// Close stops the background loop. It is safe to call it more than once.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		// Take the lock of the start, so that a start in progress ends first.
		m.loopOnce.Do(func() {})
		if m.loopCancel != nil {
			m.loopCancel()
			<-m.loopDone
		}
	})
}
