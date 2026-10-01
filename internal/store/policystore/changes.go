package policystore

import (
	"context"
	"sync"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// Changes is policy.Changes on the instance's one LISTEN connection (S08's listener, generalized
// in S12): every deedglass_policy payload is an org id, and every (re)connect is a reset signal.
type Changes struct {
	l store.ChannelListener

	once  sync.Once
	orgs  chan string
	reset chan struct{}
}

// NewChanges returns the feed. Subscribe registers the channel on l; call it before l runs.
func NewChanges(l store.ChannelListener) *Changes {
	return &Changes{l: l, orgs: make(chan string, 256), reset: make(chan struct{}, 1)}
}

var _ policy.Changes = (*Changes)(nil)

// Subscribe implements policy.Changes. A full buffer drops an org id rather than block the
// listener: the safety poll catches the rest.
func (c *Changes) Subscribe(ctx context.Context) (<-chan string, <-chan struct{}, error) {
	c.once.Do(func() {
		c.l.Listen(store.PolicyChannel, func(payload string) {
			if ids.Check(ids.Org, payload) != nil {
				return
			}
			select {
			case c.orgs <- payload:
			default:
			}
		}, func() {
			select {
			case c.reset <- struct{}{}:
			default:
			}
		})
	})
	return c.orgs, c.reset, nil
}
