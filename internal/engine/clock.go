package engine

import "time"

// timeNowMillis returns the current wall-clock time in milliseconds. It is the
// fallback clock used when callers do not inject a deterministic Clock (for
// example in production wiring).
func timeNowMillis() int64 {
	return time.Now().UnixMilli()
}
