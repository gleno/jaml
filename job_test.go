package jaml

import (
	"context"
	"errors"
	"testing"
)

func TestAJobFollowsItsLifetimeOnlyOnceSomethingWaitsOnIt(t *testing.T) {
	lifetime, endLifetime := context.WithCancelCause(context.Background())
	var unwaited = jobWithin(t.Context(), lifetime)
	var waited = jobWithin(t.Context(), lifetime)
	var waiting = waited.Done()
	endLifetime(errors.New("over"))
	<-waiting
	expectEqual(t, errors.Is(context.Cause(waited), errLifetimeOver), true)
	expectEqual(t, unwaited.Err(), nil)
	unwaited.end()
	waited.end()
	expectEqual(t, errors.Is(context.Cause(unwaited), context.Canceled), true)
}

func TestAJobEndedBeforeAnyWaitNeverFollowsItsLifetime(t *testing.T) {
	lifetime, endLifetime := context.WithCancel(context.Background())
	defer endLifetime()
	var job = jobWithin(t.Context(), lifetime)
	job.end()
	<-job.Done()
	expectEqual(t, job.unfollow == nil, true)
}
