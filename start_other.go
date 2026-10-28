//go:build !darwin

package main

import "time"

// processStart would say when the kernel started this process. Linux only
// keeps that in clock ticks (10ms), too coarse to be worth it, so --profile
// measures from when ash's own code starts.
func processStart() (time.Time, bool) { return time.Time{}, false }
