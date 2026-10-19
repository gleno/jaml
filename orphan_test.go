//go:build unix && !ios

package jaml

import (
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
		sb := mustIsolated(t)
		template := mustTemplate(t, sb, "v: ${ (() => { while (true) {} })() }\n")
		os.Stdout.WriteString("CHILD " + strconv.Itoa(childPID(t, sb)) + "\n")
		go func() { var out any; _ = template.Eval(context.Background(), &out, nil, "v") }()
		time.Sleep(200 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	self, _ := os.Executable()
	cmd := exec.Command(self, "-test.run", "TestAnOrphanedEvaluatorExits")
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("orphaned evaluator %d kept running", pid)
}
