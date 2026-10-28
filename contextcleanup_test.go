package jaml

import (
	"context"
	"errors"
	goruntime "runtime"
	"testing"
	"time"
)

type _ContextOwner struct{ expression *Expression }
type _ContextOwnerKey struct{}

type _ExpressionCause struct{ expression *Expression }

func (*_ExpressionCause) Error() string { return "the expression's origin ended" }

// The returned observer has no application reference to the dropped handle.
// Its lifetime and holder count are the resource whose cleanup we need to see.
func dropContextOwnedHandle(t *testing.T, sb *Sandbox, parent context.Context, method string, original *Expression) *_Keep {
	t.Helper()
	var owner = new(_ContextOwner)
	var origin = context.WithValue(parent, _ContextOwnerKey{}, owner)
	var err error
	switch method {
	case "expression":
		owner.expression, err = sb.MakeExpression(origin, "7")
	case "jaml":
		owner.expression, err = sb.MakeExpressionFromJaml(origin, "value: 7\n")
	case "copy":
		owner.expression, err = original.Copy(origin)
	}
	if err != nil {
		t.Fatal(err)
	}
	return owner.expression.held.keep
}

func awaitHolderCount(t *testing.T, keep *_Keep, wanted int) {
	t.Helper()
	var deadline = time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var retry = time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		goruntime.GC()
		keep.mu.Lock()
		var got = keep.holders
		keep.mu.Unlock()
		if got == wanted {
			return
		}
		select {
		case <-retry.C:
		case <-deadline.C:
			t.Fatalf("unreachable context-owned handle retained %d holders, want %d", got, wanted)
		}
	}
}

func TestCreationContextValuesReleaseDroppedHandles(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		for _, method := range []string{"expression", "jaml", "copy"} {
			t.Run(method, func(t *testing.T) {
				for _, cancellable := range []bool{false, true} {
					var name = "background"
					if cancellable {
						name = "cancelable"
					}
					t.Run(name, func(t *testing.T) {
						var parent = context.Background()
						if cancellable {
							var cancel context.CancelFunc
							parent, cancel = context.WithCancel(parent)
							defer cancel()
						}
						var original *Expression
						var remaining int
						if method == "copy" {
							original = mustExpression(t, sb, "7")
							remaining = 1
						}
						var keep = dropContextOwnedHandle(t, sb, parent, method, original)
						awaitHolderCount(t, keep, remaining)
						if original != nil {
							expectEqual(t, mustResolve[int](t, t.Context(), original), 7)
						} else if keep.lifetime.Err() == nil {
							t.Fatal("last unreachable holder did not end its unit lifetime")
						}
						// A still-live independent parent must not own the outer
						// value context merely because JAML followed its cancellation.
						expectEqual(t, parent.Err(), nil)
						goruntime.KeepAlive(original)
						goruntime.KeepAlive(parent)
					})
				}
			})
		}
	})
}

func TestCreationContextFollowersStopAfterCollection(t *testing.T) {
	var sb = mustSandbox(t, trustedKind)
	var original = mustExpression(t, sb, "7")
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var before = goruntime.NumGoroutine()
	for range 64 {
		dropContextOwnedHandle(t, sb, parent, "copy", original)
	}
	awaitHolderCount(t, original.held.keep, 1)
	var deadline = time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var retry = time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		// Allow unrelated runtime cleanup workers to start. A whole leaked
		// batch of origin followers must not remain on the still-live parent.
		var after = goruntime.NumGoroutine()
		if after <= before+16 {
			break
		}
		select {
		case <-retry.C:
		case <-deadline.C:
			t.Fatalf("collected handles left goroutines behind: before=%d after=%d", before, after)
		}
	}
	expectEqual(t, parent.Err(), nil)
	expectEqual(t, mustResolve[int](t, t.Context(), original), 7)
	goruntime.KeepAlive(parent)
	goruntime.KeepAlive(original)
}

func TestCreationContextCausesRemainPublic(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var cause = errors.New("creation caller stopped")
		origin, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		_, err := sb.MakeExpression(origin, "7")
		if !errors.Is(err, cause) {
			t.Fatalf("MakeExpression lost the caller cause: %v", err)
		}
		_, err = sb.MakeExpressionFromJaml(origin, "value: 7\n")
		if !errors.Is(err, cause) {
			t.Fatalf("MakeExpressionFromJaml lost the caller cause: %v", err)
		}
		var original = mustExpression(t, sb, "7")
		_, err = original.Copy(origin)
		if !errors.Is(err, cause) {
			t.Fatalf("Copy lost the caller cause: %v", err)
		}
		_, err = Resolve[int](origin, original)
		if !errors.Is(err, cause) {
			t.Fatalf("Resolve lost the caller cause: %v", err)
		}
	})
}

func dropWithAnExpressionCause(t *testing.T, sb *Sandbox) <-chan struct{} {
	t.Helper()
	origin, cancel := context.WithCancelCause(context.Background())
	expression, err := sb.MakeExpression(origin, "7")
	if err != nil {
		t.Fatal(err)
	}
	var collected = make(chan struct{})
	goruntime.AddCleanup(expression, func(done chan struct{}) { close(done) }, collected)
	cancel(&_ExpressionCause{expression: expression})
	return collected
}

func TestCreationContextCauseDoesNotKeepACancelledExpression(t *testing.T) {
	eachKind(t, func(t *testing.T, sb *Sandbox) {
		var collected = dropWithAnExpressionCause(t, sb)
		var deadline = time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		var retry = time.NewTicker(10 * time.Millisecond)
		defer retry.Stop()
		for {
			goruntime.GC()
			select {
			case <-collected:
				return
			case <-retry.C:
			case <-deadline.C:
				t.Fatal("creation cancellation cause kept its otherwise unreachable expression")
			}
		}
	})
}
