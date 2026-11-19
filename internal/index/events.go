package index

import (
	"sync"

	"github.com/DanielTso/ccdeck/internal/core"
)

// subscriberBuffer is the capacity of a subscription channel.
const subscriberBuffer = 16

// subscribers fans events out to subscriptions without ever blocking the
// notifier: each subscription queues events, coalescing duplicates, and a
// goroutine of its own delivers them.
type subscribers struct {
	mu     sync.Mutex
	set    map[*subscriber]struct{}
	closed bool
	wg     sync.WaitGroup
}

type subscriber struct {
	out  chan core.Event
	wake chan struct{} // capacity 1: the queue has something
	done chan struct{}
	once sync.Once

	mu    sync.Mutex
	queue []core.Event
}

func (ss *subscribers) subscribe() (<-chan core.Event, func()) {
	sub := &subscriber{
		out:  make(chan core.Event, subscriberBuffer),
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed {
		close(sub.out)
		return sub.out, func() {}
	}
	if ss.set == nil {
		ss.set = map[*subscriber]struct{}{}
	}
	ss.set[sub] = struct{}{}
	ss.wg.Go(sub.pump)
	cancel := func() {
		ss.mu.Lock()
		delete(ss.set, sub)
		ss.mu.Unlock()
		sub.stop()
	}
	return sub.out, cancel
}

// notify queues ev for every subscription.
func (ss *subscribers) notify(ev core.Event) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	for sub := range ss.set {
		sub.push(ev)
	}
}

// closeAll ends every subscription and waits for their goroutines.
func (ss *subscribers) closeAll() {
	ss.mu.Lock()
	ss.closed = true
	subs := ss.set
	ss.set = nil
	ss.mu.Unlock()
	for sub := range subs {
		sub.stop()
	}
	ss.wg.Wait()
}

func (sub *subscriber) stop() { sub.once.Do(func() { close(sub.done) }) }

func (sub *subscriber) push(ev core.Event) {
	sub.mu.Lock()
	sub.queue = coalesce(sub.queue, ev)
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default: // already woken
	}
}

// coalesce adds ev to the queue q unless it is already there. All
// session-summary changes merge into one event: for a single session while
// only one changed, for every session after that.
func coalesce(q []core.Event, ev core.Event) []core.Event {
	for i, e := range q {
		if e == ev {
			return q
		}
		if e.Kind == core.SessionsChanged && ev.Kind == core.SessionsChanged {
			q[i].SessionID = ""
			return q
		}
	}
	return append(q, ev)
}

// pump delivers queued events until the subscription ends, then closes
// the channel.
func (sub *subscriber) pump() {
	defer close(sub.out)
	for {
		select {
		case <-sub.done:
			return
		case <-sub.wake:
		}
		sub.mu.Lock()
		q := sub.queue
		sub.queue = nil
		sub.mu.Unlock()
		for _, ev := range q {
			select {
			case sub.out <- ev:
			case <-sub.done:
				return
			}
		}
	}
}
