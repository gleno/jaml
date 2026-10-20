//go:build linux && !race

package jaml

import (
	"errors"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const ceilingTestLimit = 64 << 20

var bombsPastTheCeiling = map[string]string{
	"(() => { let s = 'x'.repeat(1 << 20); for (;;) { s += s } })()":                 "exceeded the memory limit of 67108864 bytes",
	"(() => { let s = 'x'.repeat(1 << 20); for (;;) { s = `${s}${s}` } })()":         "exceeded the memory limit of 67108864 bytes",
	"(() => { let a = [1]; for (;;) { a = [...a, ...a] } })()":                       "exceeded the memory limit of 67108864 bytes",
	"new Float64Array(2 ** 28).length":                                               "exceeded the memory limit of 67108864 bytes",
	"new ArrayBuffer(2 ** 31).byteLength":                                            "exceeded the memory limit of 67108864 bytes",
	"Math.max.apply(null, {length: 2 ** 32 - 1})":                                    "exceeded the memory limit of 67108864 bytes",
	"Reflect.apply(Math.max, null, {length: 2 ** 32 - 1})":                           "exceeded the memory limit of 67108864 bytes",
	"Reflect.construct(Array, {length: 2 ** 32 - 1}).length":                         "exceeded the memory limit of 67108864 bytes",
	"Function.prototype.apply.call(Math.max, null, new Array(2 ** 32 - 1))":          "exceeded the memory limit of 67108864 bytes",
	"Array.from({length: 2 ** 32 - 1}).length":                                       "exceeded the memory limit of 67108864 bytes",
	"new Array(1 << 26).fill(1).length":                                              "exceeded the memory limit of 67108864 bytes",
	"new Array(2 ** 32 - 1).fill(0)":                                                 "exceeded the memory limit of 67108864 bytes",
	"Array.prototype.fill.call({length: 2 ** 53 - 1}, 0)":                            "exceeded the memory limit of 67108864 bytes",
	"Array.prototype.sort.call({length: 2 ** 32 - 1})":                               "exceeded the memory limit of 67108864 bytes",
	"Array.prototype.toSorted.call({length: 2 ** 31}).length":                        "exceeded the memory limit of 67108864 bytes",
	"Array(2 ** 32 - 1).join('x')":                                                   "exceeded the memory limit of 67108864 bytes",
	"Array.prototype.join.call({length: 2 ** 53 - 1}, 'é')":                          "exceeded the memory limit of 67108864 bytes",
	"JSON.stringify({}, Array(2 ** 31))":                                             "exceeded the memory limit of 67108864 bytes",
	"'x'.repeat(1 << 28).length":                                                     "exceeded the memory limit of 67108864 bytes",
	"'ab'.repeat(2 ** 62)":                                                           "the engine failed",
	"'abcd'.repeat(2 ** 53 - 1)":                                                     "the engine failed",
	"'x'.padStart(2 ** 30).length":                                                   "exceeded the memory limit of 67108864 bytes",
	"'x'.padStart(2 ** 53 - 1)":                                                      "the engine failed",
	"'x'.padEnd(2 ** 53 - 1, 'é').length":                                            "the engine failed",
	"'x'.repeat(1 << 24).split('').length":                                           "exceeded the memory limit of 67108864 bytes",
	"new Array(1 << 16).fill('x'.repeat(1 << 12)).join('').length":                   "exceeded the memory limit of 67108864 bytes",
	"'x'.repeat(1 << 16).replace('x', \"$'\".repeat(1 << 12)).length":                "exceeded the memory limit of 67108864 bytes",
	"JSON.stringify(new Array(1 << 16).fill('x'.repeat(1 << 12))).length":            "exceeded the memory limit of 67108864 bytes",
	"Object.keys('x'.repeat(1 << 24)).length":                                        "exceeded the memory limit of 67108864 bytes",
	"(() => { try { return 'x'.repeat(1 << 28) } catch (e) { return 'caught' } })()": "exceeded the memory limit of 67108864 bytes",
}

func addressAllowance() int64 {
	return addressGrowthPerLimit*ceilingTestLimit + addressSlack + addressPerThread*int64(goruntime.GOMAXPROCS(0)+goruntime.GOMAXPROCS(0))
}

type _ChildPeaks struct {
	resident atomic.Int64
	growth   atomic.Int64
	starts   sync.Map
}

func (p *_ChildPeaks) watch() func() {
	var done = make(chan struct{})
	var stopped = make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
			}
			for _, pid := range childrenOf(os.Getpid()) {
				p.sample(pid)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (p *_ChildPeaks) sample(pid int) {
	var size, resident = statusField(pid, "VmSize"), statusField(pid, "VmRSS")
	p.resident.Store(max(p.resident.Load(), resident))
	start, known := p.starts.Load(pid)
	if !known {
		if _, confined := addressCeilingOf(pid); !confined || size <= 0 {
			return
		}
		start, _ = p.starts.LoadOrStore(pid, size)
	}
	p.growth.Store(max(p.growth.Load(), size-start.(int64)))
}

func addressCeilingOf(pid int) (int64, bool) {
	limits, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/limits")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(limits), "\n") {
		if rest, found := strings.CutPrefix(line, "Max address space"); found {
			ceiling, err := strconv.ParseInt(strings.Fields(rest)[0], 10, 64)
			return ceiling, err == nil
		}
	}
	return 0, false
}

func childrenOf(parent int) []int {
	var entries, _ = os.ReadDir("/proc")
	var children []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		var fields = strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if len(fields) > 1 && fields[1] == strconv.Itoa(parent) {
			children = append(children, pid)
		}
	}
	return children
}

func statusField(pid int, key string) int64 {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(status), "\n") {
		if value, found := strings.CutPrefix(line, key+":"); found {
			kib, _ := strconv.ParseInt(strings.Fields(value)[0], 10, 64)
			return kib << 10
		}
	}
	return 0
}

func TestABombStopsAtTheAddressCeiling(t *testing.T) {
	for source, want := range bombsPastTheCeiling {
		t.Run(source, func(t *testing.T) {
			var sb = mustIsolated(t, WithMemoryLimit(ceilingTestLimit))
			var peaks = &_ChildPeaks{}
			var stop = peaks.watch()
			var out any
			var err = resolveInto(within(t, 120*time.Second), &out, mustExpression(t, sb, source))
			stop()
			t.Logf("%s: peak child growth %d MiB of address space, peak resident %d MiB, allowance %d MiB: %v", source, peaks.growth.Load()>>20, peaks.resident.Load()>>20, addressAllowance()>>20, err)
			expectError(t, err, EvaluationFailed, want)
			if peaks.growth.Load() > addressAllowance() {
				t.Fatalf("a child grew its address space by %d MiB, past its allowance of %d MiB", peaks.growth.Load()>>20, addressAllowance()>>20)
			}
			if peaks.resident.Load() > addressAllowance()+64<<20 {
				t.Fatalf("a child held %d MiB resident, past its allowance of %d MiB and its start", peaks.resident.Load()>>20, addressAllowance()>>20)
			}
			expectEqual(t, mustResolve[string](t, t.Context(), mustExpression(t, sb, "'after ' + 'the bomb'")), "after the bomb")
		})
	}
}

func TestTheSiblingsOfABombAtTheCeilingAreReplayed(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(ceilingTestLimit), WithMaxConcurrency(4))
	var sibling = mustExpression(t, sb, "(() => { let n = 0; for (let i = 0; i < 3e7; i++) n += i & 1; return n })()")
	var bomb = mustExpression(t, sb, "(() => { let s = 'x'.repeat(1 << 20); for (;;) { s += s } })()")
	var first = childPID(t, sb)
	var results = make(chan error, 3)
	for range 3 {
		go func() {
			sum, err := Resolve[float64](within(t, 120*time.Second), sibling)
			if err == nil && sum != 15e6 {
				err = errors.New("a sibling summed " + strconv.FormatFloat(sum, 'f', -1, 64))
			}
			results <- err
		}()
	}
	time.Sleep(200 * time.Millisecond)
	var out any
	expectError(t, resolveInto(within(t, 120*time.Second), &out, bomb), MemoryLimit)
	for range 3 {
		if err := <-results; err != nil {
			t.Fatalf("a sibling of the bomb failed: %v", err)
		}
	}
	if childPID(t, sb) == first {
		t.Fatalf("the bomb never took its child down")
	}
	expectEqual(t, mustResolve[float64](t, t.Context(), sibling), 15e6)
}

func TestTheAddressCeilingFollowsTheMemoryLimit(t *testing.T) {
	var sb = mustIsolated(t, WithMemoryLimit(ceilingTestLimit))
	var pid = childPID(t, sb)
	ceiling, confined := addressCeilingOf(pid)
	if !confined {
		t.Fatalf("evaluator %d has no address ceiling", pid)
	}
	var held = statusField(pid, "VmSize")
	if ceiling < held || ceiling > held+addressAllowance() {
		t.Fatalf("an address ceiling of %d MiB for a child holding %d MiB, want at most %d MiB more", ceiling>>20, held>>20, addressAllowance()>>20)
	}
}

func TestInnocentJobsOnEveryCoreNeverTripTheCeiling(t *testing.T) {
	var cores = goruntime.GOMAXPROCS(0)
	var sb = mustIsolated(t, WithMemoryLimit(smallestMemoryLimit), WithMaxConcurrency(cores))
	var document = mustJaml(t, sb, busyMillisecond)
	var child = currentChild(t, sb)
	for range 10 {
		var answers = make(chan error, cores)
		for range cores {
			go func() {
				_, err := Resolve[int](t.Context(), document.At("busy"))
				answers <- err
			}()
		}
		for range cores {
			if err := <-answers; err != nil {
				t.Fatalf("an innocent job beside %d others failed: %v", cores-1, err)
			}
		}
	}
	if !child.alive() {
		t.Fatalf("the child died under innocent work: %v", child.lost().cause)
	}
}
