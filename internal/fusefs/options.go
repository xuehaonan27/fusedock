package fusefs

import "time"

type Options struct {
	ReadOnly   bool
	AttrTTL    time.Duration
	EntryTTL   time.Duration
	Debug      bool
	VolumeName string
}
