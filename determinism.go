package jaml

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

type fixedClockKey struct{}

type randomSeedKey struct{}

const seedStream = 0x6a616d6c

type pinned struct {
	Millis int64  `json:"clock"`
	Seed   uint64 `json:"seed"`
}

func WithFixedClock(ctx context.Context, time time.Time) context.Context {
	return context.WithValue(ctx, fixedClockKey{}, time)
}

func WithRandomSeed(ctx context.Context, seed uint64) context.Context {
	return context.WithValue(ctx, randomSeedKey{}, seed)
}

func pin(ctx context.Context) pinned {
	clock, fixed := ctx.Value(fixedClockKey{}).(time.Time)
	if !fixed {
		clock = time.Now()
	}
	seed, seeded := ctx.Value(randomSeedKey{}).(uint64)
	if !seeded {
		seed = rand.Uint64()
	}
	return pinned{Millis: unixMillis(clock), Seed: seed}
}

func unixMillis(at time.Time) int64 {
	switch {
	case at.Before(time.UnixMilli(math.MinInt64)):
		return math.MinInt64
	case at.After(time.UnixMilli(math.MaxInt64)):
		return math.MaxInt64
	}
	return at.UnixMilli()
}

func (p pinned) now() time.Time {
	return time.UnixMilli(p.Millis)
}

func (p pinned) random() func() float64 {
	return rand.New(rand.NewPCG(p.Seed, seedStream)).Float64
}
