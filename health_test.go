package jaml

import (
	"encoding/json"
	goruntime "runtime"
	"sync"
	"testing"
	"time"
)

func TestASandboxReportsItsChildsResidentMemoryCeilingAndRestarts(t *testing.T) {
	var sb = mustIsolated(t)
	var quick = mustJaml(t, sb, spinning).At("quick")
	var reported []Usage
	expectEqual(t, mustResolve[int](t, WithMeter(t.Context(), func(used Usage) { reported = append(reported, used) }), quick), 2)
	var health = sb.Health()
	t.Logf("health after one resolve: %+v, usage %+v", health, reported)
	if health.Restarts != 0 {
		t.Fatalf("a sandbox whose child never died counts %d restarts", health.Restarts)
	}
	if goruntime.GOOS == "linux" {
		if len(reported) != 1 || reported[0].Resident <= 0 || health.Resident != reported[0].Resident {
			t.Fatalf("on Linux a resolve reports the child's resident memory and the sandbox holds the last reading: usage %+v, health %+v", reported, health)
		}
		if health.AddressCeiling != 0 && health.AddressCeiling <= health.Resident {
			t.Fatalf("the child's address-space ceiling %d is not above its resident %d", health.AddressCeiling, health.Resident)
		}
	}
	var child = currentChild(t, sb)
	killChild(t, sb)
	<-child.ended
	expectEqual(t, mustResolve[int](t, t.Context(), quick), 2)
	if restarted := sb.Health(); restarted.Restarts != 1 {
		t.Fatalf("a child replaced once counts %d restarts", restarted.Restarts)
	}
}

func TestATrustedSandboxReportsItsOwnResidentMemoryAndNoChild(t *testing.T) {
	sb, err := MakeTrustedSandbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var health = sb.Health()
	if health.AddressCeiling != 0 || health.Restarts != 0 {
		t.Fatalf("a trusted sandbox has no child to cap or restart: %+v", health)
	}
	if goruntime.GOOS == "linux" && health.Resident <= 0 {
		t.Fatalf("on Linux a trusted sandbox reports this process's resident memory, got %+v", health)
	}
}

func TestAUsageFrameCarriesTheResidentMemoryOnlyWhenKnown(t *testing.T) {
	var f = _Frames{limit: 1 << 10}
	expectEqual(t, string(f.usage(7, Usage{CPU: time.Millisecond, Wall: 2 * time.Millisecond})), `{"job":7,"usage":{"cpu":1000000,"wall":2000000}}`)
	expectEqual(t, string(f.usage(7, Usage{CPU: time.Millisecond, Wall: 2 * time.Millisecond, Resident: 3 << 20})), `{"job":7,"usage":{"cpu":1000000,"wall":2000000,"rss":3145728}}`)
	var incoming _Reply
	if err := json.Unmarshal(f.usage(7, Usage{Resident: 42}), &incoming); err != nil {
		t.Fatal(err)
	}
	expectEqual(t, incoming.Usage.measured(), Usage{Resident: 42})
}

func TestHealthIncludesTheChildOnAnExclusiveReplay(t *testing.T) {
	var life = &_Life{ended: make(chan struct{}), board: &_Switchboard{}}
	life.board.resident.Store(123)
	life.ceiling.Store(456)
	var d = &_Dispatcher{mode: recovering, child: _Ready{life: life}, running: 1, restarts: 2}
	if _, owned := d.lend(life).(_Owned); !owned {
		t.Fatal("recovery did not take an exclusive lease")
	}
	expectEqual(t, d.health(), Health{Resident: 123, AddressCeiling: 456, Restarts: 2})
	close(life.ended)
	d.lose(life)
	expectEqual(t, d.health(), Health{Restarts: 2})
	var replacement = &_Life{ended: make(chan struct{}), board: &_Switchboard{}}
	replacement.board.resident.Store(789)
	d.child = _Ready{life: replacement}
	d.lose(life)
	expectEqual(t, d.health(), Health{Resident: 789, Restarts: 2})
}

func TestAnExclusiveChildRetiresBeforeTheNextReplayIsAdmitted(t *testing.T) {
	for _, unexpected := range []bool{false, true} {
		name := "released"
		if unexpected {
			name = "died"
		}
		t.Run(name, func(t *testing.T) {
			fakeLife := func() (*_Life, func()) {
				reaped := make(chan struct{})
				close(reaped)
				life := &_Life{ended: make(chan struct{}), reaped: reaped, board: &_Switchboard{}}
				end := sync.OnceFunc(func() { close(life.ended) })
				t.Cleanup(end)
				return life, end
			}
			old, endOld := fakeLife()
			replacement, endReplacement := fakeLife()
			d := &_Dispatcher{mode: recovering, running: 1, limit: 1, child: _Ready{life: old}, start: func() (*_Life, error) { return replacement, nil }}
			held := d.lend(old)
			next := &_Errand{caller: t.Context(), answers: make(chan _Admission, 1)}
			d.suspects = []*_Errand{next}
			if unexpected {
				endOld()
				d.lose(old)
			}
			d.release(held)
			if !unexpected {
				d.mu.Lock()
				if _, exclusive := d.child.(_Exclusive); !exclusive || d.running != 0 {
					t.Errorf("the retiring child was forgotten before it ended: child %T, running %d", d.child, d.running)
				}
				d.mu.Unlock()
			}
			endOld()
			d.mu.Lock()
			d.lose(old)
			d.dispatch()
			d.mu.Unlock()
			var admitted _Admission
			select {
			case admitted = <-next.answers:
			case <-time.After(time.Second):
				t.Fatal("retiring the owned child never admitted the next replay")
			}
			if owned, ok := admitted.lease.(_Owned); !ok || owned.life != replacement {
				t.Fatalf("the next replay was not admitted alone on its replacement: %+v", admitted)
			}
			d.mu.Lock()
			d.lose(old)
			if d.currentLife() != replacement || d.mode != recovering || d.running != 1 {
				t.Errorf("a stale death notification erased the replacement: child %T, mode %v, running %d", d.child, d.mode, d.running)
			}
			d.mu.Unlock()
			d.release(admitted.lease)
			endReplacement()
		})
	}
}
