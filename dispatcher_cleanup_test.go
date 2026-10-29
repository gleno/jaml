package jaml

import (
	"context"
	"errors"
	goruntime "runtime"
	"slices"
	"testing"
	"time"
)

type _DiscardedCallerKey struct{}

func queueAbandonedCaller(d *_Dispatcher) (<-chan _Admission, <-chan struct{}) {
	var payload = new([64]byte)
	var collected = make(chan struct{})
	goruntime.AddCleanup(payload, func(done chan struct{}) { close(done) }, collected)
	caller, cancel := context.WithCancel(context.WithValue(context.Background(), _DiscardedCallerKey{}, payload))
	cancel()
	var answers = make(chan _Admission, 1)
	d.suspects = append(d.suspects, &_Errand{caller: caller, answers: answers})
	return answers, collected
}

func TestDiscardedSuspectsReleaseTheirCallerValues(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		var name = "all"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			// An owned replay is still running while its attribution discards
			// abandoned suspects, so surviving requests must remain queued.
			var d = &_Dispatcher{mode: recovering, running: 1}
			defer goruntime.KeepAlive(d)
			var survivors []*_Errand
			var collected []<-chan struct{}
			var rejected []<-chan _Admission
			for range 2 {
				if mixed {
					var survivor = &_Errand{caller: context.Background(), answers: make(chan _Admission, 1)}
					survivors = append(survivors, survivor)
					d.suspects = append(d.suspects, survivor)
				}
				answer, done := queueAbandonedCaller(d)
				rejected = append(rejected, answer)
				collected = append(collected, done)
			}
			d.dropTheSuspectsWhoseCallersLeft()
			if !slices.Equal(d.suspects, survivors) {
				t.Fatal("discarding abandoned callers changed the surviving replay order")
			}
			for _, answered := range rejected {
				select {
				case result := <-answered:
					if !errors.Is(result.err, context.Canceled) || result.lease != nil {
						t.Fatalf("discarded caller received %+v", result)
					}
				default:
					t.Fatal("discarded caller was not released from admission")
				}
			}
			var deadline = time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			var retry = time.NewTicker(10 * time.Millisecond)
			defer retry.Stop()
			for _, done := range collected {
				for {
					goruntime.GC()
					select {
					case <-done:
						goto next
					case <-retry.C:
					case <-deadline.C:
						t.Fatal("the retained dispatcher kept a discarded caller's context values alive")
					}
				}
			next:
			}
		})
	}
}
