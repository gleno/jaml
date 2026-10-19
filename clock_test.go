package jaml

import (
	"math"
	"testing"
	"time"
)

func TestThePinnedClockIsTheSameInstantInBothKinds(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no zone data: %v", err)
	}
	var clocks = map[string]time.Time{
		"year -1":           time.Date(-1, 6, 1, 0, 0, 0, 0, time.UTC),
		"year 10000":        time.Date(10000, 6, 1, 0, 0, 0, 0, time.UTC),
		"past the JS range": time.UnixMilli(8.64e15 + 1),
		"before it":         time.UnixMilli(-8.64e15 - 1),
		"1800 in New York":  time.Date(1800, 1, 1, 0, 0, 0, 0, newYork),
		"odd fixed zone":    time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("odd", 3600+17)),
		"sub-millisecond":   time.Unix(0, 1_500_000),
		"saturating":        time.Unix(math.MaxInt64, 0),
	}
	for name, timestamp := range clocks {
		t.Run(name, func(t *testing.T) {
			var seen = map[string][]any{}
			for _, kind := range sandboxKinds {
				var compiled = mustExpression(t, sharedSandbox(t, kind), "[Date.now(), String(new Date())]")
				var drawn []any
				if err := resolveInto(WithFixedClock(t.Context(), timestamp), &drawn, compiled); err != nil {
					t.Fatalf("%s: %v", kind.name, err)
				}
				seen[kind.name] = drawn
			}
			expectEqual(t, seen[isolatedKind.name], seen[trustedKind.name])
		})
	}
}
