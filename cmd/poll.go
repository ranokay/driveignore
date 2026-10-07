package cmd

import "time"

// The watch and guard loops poll on an adaptive interval: fast while changes
// keep flowing and backing off towards pollMaxInterval while idle or after a
// failed pass, so an unused or unhealthy folder is not hammered.
const (
	pollBaseInterval = 2 * time.Second
	pollMaxInterval  = 60 * time.Second
)

// nextPollInterval picks the delay before the next pass: base after a pass
// that made progress, doubling towards the maximum while idle or after a
// failure, never going below base.
func nextPollInterval(prev, base time.Duration, hadActions, failed bool) time.Duration {
	if !failed && hadActions {
		return base
	}
	next := prev * 2
	if next < base {
		next = base
	}
	if next > pollMaxInterval {
		next = pollMaxInterval
	}
	return next
}
