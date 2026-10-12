package junos

import "time"

// SetReleaseTimeout changes how long Change.Apply gives the release of the
// candidate, for the test of a release that gets no answer.  It returns what
// puts the time back.
func SetReleaseTimeout(d time.Duration) (restore func()) {
	old := releaseTimeout
	releaseTimeout = d
	return func() { releaseTimeout = old }
}

// SetLockRetry changes how long Change.Apply waits between two requests for
// a lock that another session holds, for the tests of LockWait.  It returns
// what puts the time back.
func SetLockRetry(d time.Duration) (restore func()) {
	old := lockRetry
	lockRetry = d
	return func() { lockRetry = old }
}

// JunosJSON is the rewrite a JSON document goes through before it is loaded.
var JunosJSON = junosJSON
