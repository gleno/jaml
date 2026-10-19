//go:build unix && !ios

package jaml

import (
	"context"
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
	var hours = map[string]int{"Asia/Tokyo": 9, "America/Sao_Paulo": 21}
	var locations = map[string]*time.Location{}
	for zone := range hours {
		location, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone data for %s: %v", zone, err)
		}
		locations[zone] = location
	}
	survivesInAStackCappedChild(t, func(t *testing.T) {
		for zone, hour := range hours {
			var trusted, secure = hoursAtEpochInBothKinds(t, locations[zone])
			expectEqual(t, trusted, hour)
			expectEqual(t, secure, trusted)
		}
	})
}

func TestTheEvaluatorHasItsOwnProcessGroup(t *testing.T) {
	var pid = childPID(t, mustIsolated(t))
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	expectEqual(t, group, pid)
}

func TestTheMemoryLimitBoundsGrowthOverTheHostsHeap(t *testing.T) {
	var document = mustJaml(t, mustIsolated(t, WithMemoryLimit(ballastLimit)), "v: \"${ 'x'.repeat(24 << 20).length }\"\n")
	expectEqual(t, isolatedEvalAs[int](t, document, nil, "v"), 24<<20)
	expectEqual(t, len(hostBallast), 0)
}

func TestOnlyANamedZoneIsForwarded(t *testing.T) {
	survivesInAStackCappedChild(t, func(t *testing.T) {
		time.Local = time.FixedZone("Somewhere", 5*3600)
		expectEqual(t, forwardableZone(), "")
		time.Local = time.UTC
		expectEqual(t, forwardableZone(), "UTC")
	})
}

const tokyoZoneFile = "/usr/share/zoneinfo/Asia/Tokyo"

func hoursAtEpochInBothKinds(t *testing.T, local *time.Location) (int, int) {
	t.Helper()
	time.Local = local
	var text = "h: ${ new Date(0).getHours() }\n"
	var trusted = evalAs[int](t, mustJaml(t, mustSandbox(t, trustedKind), text), nil, "h")
	if err := isolationSupported(); err != nil {
		t.Skip(err)
	}
	ctx, end := context.WithCancel(t.Context())
	defer end()
	secure, err := isolatedKind.make(ctx)
	if err != nil {
		t.Fatalf("making a secure sandbox: %v", err)
	}
	var isolated = isolatedEvalAs[int](t, mustJaml(t, secure, text), nil, "h")
	var child = currentChild(t, secure)
	end()
	expectDeath(t, child)
	return trusted, isolated
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
	survivesInAStackCappedChild(t, func(t *testing.T) {
		trusted, secure := hoursAtEpochInBothKinds(t, local)
		expectEqual(t, trusted, 9)
		expectEqual(t, secure, trusted)
	})
}

func TestAZoneFoundThroughZoneinfoFollowsTheHost(t *testing.T) {
	if os.Getenv(stackCappedChild) != t.Name() {
		var zoneinfo = t.TempDir()
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
