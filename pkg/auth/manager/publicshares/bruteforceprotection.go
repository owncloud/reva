package publicshares

import (
	"sync"
	"time"
)

// attemptData contains the data we need to store for each failed attempt.
// Right now, only the timestamp of the failed attempt is needed
type attemptData struct {
	Timestamp int64
}

// BruteForceProtection implements a rate-limit-based protection for the
// public shares.
// Given a time duration (10 minutes, for example), a maximum of X failed
// attempts are allowed. If that rate is reached, access to the public link
// should be blocked until the rate decreases.
// Note that the time the link should be blocked is undefined and will be
// somewhere between 0 and the given duration
type BruteForceProtection struct {
	rwmutex     *sync.RWMutex
	attemptMap  map[string][]*attemptData
	timeGap     time.Duration
	maxAttempts int
}

// NewBruteForceProtection creates a new instance of BruteForceProtection
func NewBruteForceProtection(timeGap time.Duration, maxAttempts int) *BruteForceProtection {
	if timeGap == 0 {
		timeGap = 1 * time.Hour
	}

	if maxAttempts == 0 {
		maxAttempts = 5
	}

	return &BruteForceProtection{
		rwmutex:     &sync.RWMutex{},
		attemptMap:  make(map[string][]*attemptData),
		timeGap:     timeGap,
		maxAttempts: maxAttempts,
	}
}

// AddAttempt register a new failed attempt for the provided public share
func (bfp *BruteForceProtection) AddAttempt(shareToken string) {
	bfp.rwmutex.Lock()
	defer bfp.rwmutex.Unlock()

	attempt := &attemptData{
		Timestamp: time.Now().Unix(),
	}

	bfp.attemptMap[shareToken] = append(bfp.attemptMap[shareToken], attempt)

	// clean data if needed
	bfp.checkProtection(shareToken)
}

// Verify checks if you're allowed to access to the public share based on the
// registered failed attempts.
// If the registered failed attempts are lower or equal than the maximum
// allowed, this method will return true, meaning you're allowed to access
// If the failed attempts are greater than the maximum allowed, this method
// will return false. Note that there could be failed attempts that are no
// longer applicable, so if this method return false you should call the
// "CleanInfo" method to remove obsolete attempts.
func (bfp *BruteForceProtection) Verify(shareToken string) bool {
	bfp.rwmutex.RLock()
	defer bfp.rwmutex.RUnlock()

	attemptList, ok := bfp.attemptMap[shareToken]
	if !ok {
		// no failed attempt registered
		return true
	}

	return len(attemptList) <= bfp.maxAttempts
}

// CleanInfo will remove obsolete failed attempts for the public share and
// return whether you're allowed to access to the public share after cleaning
// the info.
func (bfp *BruteForceProtection) CleanInfo(shareToken string) bool {
	bfp.rwmutex.Lock()
	defer bfp.rwmutex.Unlock()

	return bfp.checkProtection(shareToken)
}

// checkProtection return true if the check is successful and you're allowed
// to access, false otherwise
func (bfp *BruteForceProtection) checkProtection(shareToken string) bool {
	// write lock should have been acquired before calling this method
	minTimestamp := time.Now().Add(-1 * bfp.timeGap).Unix()

	attemptList, ok := bfp.attemptMap[shareToken]
	if !ok {
		return true
	}

	var index int
	for index = 0; index < len(attemptList); index++ {
		if attemptList[index].Timestamp >= minTimestamp {
			break
		}
	}

	if index > len(attemptList) {
		// the attempt info is obsolete
		delete(bfp.attemptMap, shareToken)
		return true
	} else if index != 0 {
		// remove obsolete info and leave only useful one
		bfp.attemptMap[shareToken] = bfp.attemptMap[shareToken][index:]
	}

	return len(bfp.attemptMap[shareToken]) <= bfp.maxAttempts
}
