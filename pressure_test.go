//go:build unix && !ios

package jaml

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAForeignKillIsBlamedOnMemoryOnlyPastThreeQuartersLive(t *testing.T) {
	var cases = map[string]struct {
		heldMiB int
		memory  bool
		want    string
	}{
		"under half live":          {28, false, "killed from outside jaml"},
		"past three quarters live": {60, true, "$: exceeded the memory limit of 67108864 bytes"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var sb = mustIsolated(t, WithMemoryLimit(64<<20))
			var holding = mustExpression(t, sb, "(() => { const keep = 'x'.repeat("+strconv.Itoa(c.heldMiB)+" << 20); let n = 0; for (let j = 0; ; j++) n += keep.length & j; })()")
			var first = childPID(t, sb)
			var busy = resident(t, first) + c.heldMiB<<20*9/10
			var verdict = make(chan error, 1)
			go func() {
				var out any
				verdict <- resolveInto(within(t, 60*time.Second), &out, holding)
			}()
			for pid := first; pid != 0; {
				awaitHeld(t, sb, pid, busy, c.memory, verdict)
				time.Sleep(time.Second)
				var known = ourChildren(t)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				pid = awaitNewChild(t, known, verdict)
			}
			var err = <-verdict
			if errors.Is(err, MemoryLimit) != c.memory || err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v (memory %v), want %q (memory %v)", err, errors.Is(err, MemoryLimit), c.want, c.memory)
			}
		})
	}
}

func awaitNewChild(t *testing.T, known []int, verdict chan error) int {
	t.Helper()
	var deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-verdict:
			verdict <- err
			return 0
		default:
		}
		for _, pid := range ourChildren(t) {
			if !slices.Contains(known, pid) {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no evaluator replaced the killed one")
	return 0
}

func ourChildren(t *testing.T) []int {
	t.Helper()
	var listing = exec.Command("ps", "-axo", "pid=,ppid=")
	out, err := listing.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		var pid, parent int
		if _, err := fmt.Sscan(line, &pid, &parent); err != nil {
			t.Fatalf("ps said %q", line)
		}
		if parent == os.Getpid() && pid != listing.Process.Pid {
			pids = append(pids, pid)
		}
	}
	return pids
}

func awaitHeld(t *testing.T, sb *Sandbox, pid int, bytes int, pressured bool, verdict chan error) {
	t.Helper()
	var deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resident(t, pid) >= bytes || pressured && lifeOf(t, sb, pid).pressured() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var held = resident(t, pid)
	var life = lifeOf(t, sb, pid)
	var seen = "no verdict yet"
	select {
	case err := <-verdict:
		seen = fmt.Sprintf("verdict %v", err)
	default:
	}
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	var phase = fmt.Sprintf("%T in mode %d", d.child, d.mode)
	d.mu.Unlock()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("evaluator %d never held %d bytes: it holds %d, the dispatcher holds %s, the child said %q; %s", pid, bytes, held, phase, life.said(), seen)
}

func lifeOf(t *testing.T, sb *Sandbox, pid int) *_Life {
	t.Helper()
	var d = dispatcherOf(t, sb)
	d.mu.Lock()
	defer d.mu.Unlock()
	current, isReady := d.child.(_Ready)
	if !isReady || current.life.cmd.Process.Pid != pid {
		return nil
	}
	return current.life
}

func (l *_Life) pressured() bool {
	return l != nil && l.cmd.Stderr.(*_HeadBuffer).holds(pressureMarker)
}

func (l *_Life) said() string {
	if l == nil {
		return ""
	}
	var head = l.cmd.Stderr.(*_HeadBuffer)
	head.mu.Lock()
	defer head.mu.Unlock()
	return string(head.kept)
}

func resident(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps %d: %v", pid, err)
	}
	kib, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps %d said %q", pid, out)
	}
	return kib << 10
}
