package mountapi

import (
	"context"
	"time"
)

type Options struct {
	ReadSlots chan struct{}
	Ready     func()
	// RegisterUnmount publishes a retryable native detach operation before Ready.
	// An error leaves the mounted session serving requests.
	RegisterUnmount func(func(context.Context) error)
	ReadOnly        bool
	AttrTTL         time.Duration
	EntryTTL        time.Duration
	Debug           bool
	VolumeName      string
}
