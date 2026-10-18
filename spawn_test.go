//go:build unix && !ios

package jaml

import (
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const ballastLimit = 48<<20 + 5

var hostBallast = buildHostBallast()

func buildHostBallast() []byte {
	if os.Getenv(evaluatorMarker) != "1" || os.Getenv(evaluatorMemoryLimit) != strconv.Itoa(ballastLimit) {
		return nil
	}
	return make([]byte, 64<<20)
}

func TestSecureAndTrustedShareTheLocalZone(t *testing.T) {
	hours := map[string]int{"Asia/Tokyo": 9, "America/Sao_Paulo": 21}
	for zone, hour := range hours {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone data for %s: %v", zone, err)
		}
		previous := time.Local
		time.Local = location
		text := "h: ${ new Date(0).getHours() }\n"
		trusted := evalAs[int](t, mustTemplate(t, text), nil, "h")
		secure := secureEvalAs[int](t, mustSecure(t, text), nil, "h")
		time.Local = previous
		expectEqual(t, trusted, hour)
		expectEqual(t, secure, trusted)
	}
}

func TestTheEvaluatorHasItsOwnProcessGroup(t *testing.T) {
	pid := evaluatorPID(mustSecure(t, "v: ${ 1 }\n"))
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, group, pid)
}

func TestTheMemoryLimitBoundsGrowthOverTheHostsHeap(t *testing.T) {
	template := mustSecure(t, "v: \"${ 'x'.repeat(24 << 20).length }\"\n", WithMemoryLimit(ballastLimit))
	expectEqual(t, secureEvalAs[int](t, template, nil, "v"), 24<<20)
	expectEqual(t, len(hostBallast), 0)
}

func TestOnlyANamedZoneIsForwarded(t *testing.T) {
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	time.Local = time.FixedZone("Somewhere", 5*3600)
	expectEqual(t, forwardableZone(), "")
	time.Local = time.UTC
	expectEqual(t, forwardableZone(), "UTC")
}
