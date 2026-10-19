package jaml

import (
	"context"
	"math/rand/v2"
	"time"
)

type fixedClockKey struct{}

type randomSeedKey struct{}

const seedStream = 0x6a616d6c

type pinned struct {
	Clock time.Time `json:"clock"`
	Seed  uint64    `json:"seed"`
}

func WithFixedClock(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, fixedClockKey{}, at)
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
	return pinned{Clock: clock.Round(0), Seed: seed}
}

func (p pinned) now() time.Time {
	return p.Clock
}

func (p pinned) random() func() float64 {
	return rand.New(rand.NewPCG(p.Seed, seedStream)).Float64
}
