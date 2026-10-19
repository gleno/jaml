package jaml

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

type _FixedClockKey struct{}

type _RandomSeedKey struct{}

const seedStream = 0x6a616d6c

type _Pinned struct {
	Millis int64  `json:"clock"`
	Seed   uint64 `json:"seed"`
}

func WithFixedClock(ctx context.Context, time time.Time) context.Context {
	return context.WithValue(ctx, _FixedClockKey{}, time)
}

func WithRandomSeed(ctx context.Context, seed uint64) context.Context {
	return context.WithValue(ctx, _RandomSeedKey{}, seed)
}

func pin(ctx context.Context) _Pinned {
	clock, fixed := ctx.Value(_FixedClockKey{}).(time.Time)
	if !fixed {
		clock = time.Now()
	}
	seed, seeded := ctx.Value(_RandomSeedKey{}).(uint64)
	if !seeded {
		seed = rand.Uint64()
	}
	return _Pinned{Millis: unixMillis(clock), Seed: seed}
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

func (p _Pinned) now() time.Time {
	return time.UnixMilli(p.Millis)
}

func (p _Pinned) random() func() float64 {
	return rand.New(rand.NewPCG(p.Seed, seedStream)).Float64
}
