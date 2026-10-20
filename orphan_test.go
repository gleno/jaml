//go:build unix && !ios

package jaml

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAnOrphanedEvaluatorExits(t *testing.T) {
	if os.Getenv("JAML_ORPHAN_PARENT") == "1" {
		var sb = mustIsolated(t)
		var document = mustJaml(t, sb, "v: ${ (() => { while (true) {} })() }\n")
		os.Stdout.WriteString("CHILD " + strconv.Itoa(childPID(t, sb)) + "\n")
		go func() { var out any; _ = resolveInto(context.Background(), &out, document.At("v")) }()
		time.Sleep(200 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	self, _ := os.Executable()
	var cmd = exec.Command(self, "-test.run", "TestAnOrphanedEvaluatorExits")
	cmd.Env = append(os.Environ(), "JAML_ORPHAN_PARENT=1")
	output, _ := cmd.Output()
	var pid int
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "CHILD ") {
			pid, _ = strconv.Atoi(strings.TrimPrefix(line, "CHILD "))
		}
	}
	if pid == 0 {
		t.Fatalf("no child pid in %q", output)
	}
	var deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if gone(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("orphaned evaluator %d kept running", pid)
}

func gone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	var fields = strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	return len(fields) > 0 && fields[0] == "Z"
}
