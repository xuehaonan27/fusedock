package mountapi

import "time"

type Options struct {
	ReadSlots  chan struct{}
	Ready      func()
	ReadOnly   bool
	AttrTTL    time.Duration
	EntryTTL   time.Duration
	Debug      bool
	VolumeName string
}
