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
		trusted := evalAs[int](t, mustTemplate(t, mustSandbox(t, trustedKind), text), nil, "h")
		secure := isolatedEvalAs[int](t, mustTemplate(t, mustIsolated(t), text), nil, "h")
		time.Local = previous
		expectEqual(t, trusted, hour)
		expectEqual(t, secure, trusted)
	}
}

func TestTheEvaluatorHasItsOwnProcessGroup(t *testing.T) {
	pid := childPID(t, mustIsolated(t))
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, group, pid)
}

func TestTheMemoryLimitBoundsGrowthOverTheHostsHeap(t *testing.T) {
	template := mustTemplate(t, mustIsolated(t, WithMemoryLimit(ballastLimit)), "v: \"${ 'x'.repeat(24 << 20).length }\"\n")
	expectEqual(t, isolatedEvalAs[int](t, template, nil, "v"), 24<<20)
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

const tokyoZoneFile = "/usr/share/zoneinfo/Asia/Tokyo"

func hoursAtEpochInBothKinds(t *testing.T, local *time.Location) (int, int) {
	t.Helper()
	previous := time.Local
	time.Local = local
	defer func() { time.Local = previous }()
	text := "h: ${ new Date(0).getHours() }\n"
	return evalAs[int](t, mustTemplate(t, mustSandbox(t, trustedKind), text), nil, "h"), isolatedEvalAs[int](t, mustTemplate(t, mustIsolated(t), text), nil, "h")
}

func tokyoZoneData(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(tokyoZoneFile)
	if err != nil {
		t.Skipf("no zone data at %s: %v", tokyoZoneFile, err)
	}
	return data
}

func TestAZoneNamedByItsFileFollowsTheHost(t *testing.T) {
	local, err := time.LoadLocationFromTZData(tokyoZoneFile, tokyoZoneData(t))
	if err != nil {
		t.Fatal(err)
	}
	trusted, secure := hoursAtEpochInBothKinds(t, local)
	expectEqual(t, trusted, 9)
	expectEqual(t, secure, trusted)
}

func TestAZoneFoundThroughZoneinfoFollowsTheHost(t *testing.T) {
	if os.Getenv(stackCappedChild) != t.Name() {
		zoneinfo := t.TempDir()
		if err := os.MkdirAll(zoneinfo+"/Jaml", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(zoneinfo+"/Jaml/Somewhere", tokyoZoneData(t), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZONEINFO", zoneinfo)
	}
	survivesInAStackCappedChild(t, func(t *testing.T) {
		local, err := time.LoadLocation("Jaml/Somewhere")
		if err != nil {
			t.Fatal(err)
		}
		trusted, secure := hoursAtEpochInBothKinds(t, local)
		expectEqual(t, trusted, 9)
		expectEqual(t, secure, trusted)
	})
}
