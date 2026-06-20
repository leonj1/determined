package clients

import "time"

// SystemClock reads the real wall clock.
type SystemClock struct{}

// NewSystemClock constructs a SystemClock.
func NewSystemClock() SystemClock { return SystemClock{} }

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }
