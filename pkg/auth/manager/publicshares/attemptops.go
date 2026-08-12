package publicshares

import (
	"time"
)

// InsertAttempt ensures the attempt is added in the right possition.
// Since multiple attempts can happen very closely, the attempt at time 73
// might have been registered before the attempt at time 72
// Multiple attempts can have the same timestamp but different IDs. If a new
// timestamp has the same timestamp than any other, the new attempt will
// appear first.
// Attempts with the same ID and timestamp will be considered as duplicates,
// so the attempt won't be re-inserted.
func InsertAttempt(attemptList []*attemptData, attempt *attemptData) []*attemptData {
	if attempt == nil {
		return attemptList
	}

	// the expected case is that the attempt's timestamp is newer than
	// the last registered attempt, so we can just add it at the end.
	if len(attemptList) == 0 || attemptList[len(attemptList)-1].Timestamp < attempt.Timestamp {
		return append(attemptList, attempt)
	}

	// the attempt should be added close to the end, so start checking
	// from the last item
	i := len(attemptList)
	for i > 0 {
		i = i - 1
		if attemptList[i].Timestamp == attempt.Timestamp && attemptList[i].ID == attempt.ID {
			// attempt already inserted
			return attemptList
		}

		if attemptList[i].Timestamp < attempt.Timestamp {
			i = i + 1 // adjust the index to point where the attempt should be added
			break
		}
	}

	return append(attemptList[:i], append([]*attemptData{attempt}, attemptList[i:]...)...)
}

// CleanAttempts will remove obsolete attempt data
func CleanAttempts(attemptList []*attemptData, timeGap time.Duration) []*attemptData {
	minTimestamp := time.Now().Add(-1 * timeGap).Unix()

	var index int
	for index = 0; index < len(attemptList); index++ {
		if attemptList[index].Timestamp >= minTimestamp {
			break
		}
	}

	return attemptList[index:]
}

// IsSubsetOfAttempts checks if "b" is a subset of "a". Both "a" and "b" MUST
// be sorted
// Note: an empty list is a subset of any list, including other empty list.
// This means that if "b" is empty, this function will return true
// Note2: using nil in either "a" or "b" is discouraged. It will return true
// in both cases, but do NOT rely on this behavior.
func IsSubsetOfAttempts(a, b []*attemptData) bool {
	if len(b) > len(a) {
		return false
	}

	aindex := 0
	for _, bvalue := range b {
		for aindex < len(a) {
			if a[aindex].Timestamp == bvalue.Timestamp && a[aindex].ID == bvalue.ID {
				break
			}
			if a[aindex].Timestamp > bvalue.Timestamp {
				return false
			}

			aindex = aindex + 1
			if aindex >= len(a) {
				return false
			}
		}
	}
	return true
}
