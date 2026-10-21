package jaml

import (
	"encoding/json"
	goruntime "runtime"
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
