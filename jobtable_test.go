package jaml

import (
	"sync"
	"testing"
	"time"
)

type _StuckLog struct {
	mu   sync.Mutex
	jobs []uint64
}

func (s *_StuckLog) note(job uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
}

func (s *_StuckLog) noted() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.jobs...)
}

func watchedTable() (*_JobTable, *_StuckLog) {
	var log = &_StuckLog{}
	return newJobTable(log.note), log
}

func builtUnit(t *testing.T) _Servable {
	t.Helper()
	built, err := _Blueprint{Written: "v: 1\n", Null: Loose, Undefined: Strict}.build(t.Context())
	if err != nil {
		t.Fatalf("building: %v", err)
	}
	return built
}

func expectResident(t *testing.T, table *_JobTable, want int) {
	t.Helper()
	table.mu.Lock()
	defer table.mu.Unlock()
	if len(table.jobs) != want {
		t.Fatalf("the child holds %d jobs, want %d: %v", len(table.jobs), want, table.jobs)
	}
}

func expectNeverStuck(t *testing.T, log *_StuckLog) {
	t.Helper()
	time.Sleep(2 * cancelGrace)
	if stuck := log.noted(); len(stuck) != 0 {
		t.Fatalf("jobs %v were judged stuck after they had returned", stuck)
	}
}

func TestAForgetRacingAFinishingBuildLeavesNothingResident(t *testing.T) {
	var table, log = watchedTable()
	ctx, err := table.begin(1)
	if err != nil {
		t.Fatal(err)
	}
	var built = builtUnit(t)
	table.forget(1)
	if ctx.Err() == nil {
		t.Fatal("a forget during its build left the build running")
	}
	if table.keep(1, built) {
		t.Fatal("a build forgotten before its store was kept")
	}
	expectResident(t, table, 0)
	if table.holding(1) != nil {
		t.Fatal("a forgotten build is still servable")
	}
	expectNeverStuck(t, log)
}

func TestAForgetAfterTheStoreDropsTheUnit(t *testing.T) {
	var table, log = watchedTable()
	if _, err := table.begin(1); err != nil {
		t.Fatal(err)
	}
	if !table.keep(1, builtUnit(t)) {
		t.Fatal("a build nobody forgot was refused")
	}
	if table.holding(1) == nil {
		t.Fatal("a kept build is not servable")
	}
	table.forget(1)
	table.forget(1)
	expectResident(t, table, 0)
	expectNeverStuck(t, log)
}

func TestACancelBeforeArrivalStartsTheJobStopped(t *testing.T) {
	var table, log = watchedTable()
	table.cancel(2)
	ctx, err := table.begin(2)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("a job stopped before it arrived started running")
	}
	if !table.settle(2) {
		t.Fatal("a job stopped before it arrived settled as never told")
	}
	expectResident(t, table, 0)
	expectNeverStuck(t, log)
}

func TestAStaleCancelOrForgetLeavesNoTrace(t *testing.T) {
	var table, log = watchedTable()
	if _, err := table.begin(3); err != nil {
		t.Fatal(err)
	}
	table.settle(3)
	table.cancel(3)
	table.forget(1)
	expectResident(t, table, 0)
	expectNeverStuck(t, log)
}

func TestAJobStillWorkingAfterItsGraceIsStuck(t *testing.T) {
	var table, log = watchedTable()
	if _, err := table.begin(4); err != nil {
		t.Fatal(err)
	}
	if _, err := table.begin(5); err != nil {
		t.Fatal(err)
	}
	table.cancel(4)
	time.Sleep(cancelGrace / 2)
	table.cancel(4)
	time.Sleep(cancelGrace/2 + 100*time.Millisecond)
	if stuck := log.noted(); len(stuck) != 1 || stuck[0] != 4 {
		t.Fatalf("judged stuck: %v, want only job 4", stuck)
	}
}

func TestAJobThatReturnsWithinItsGraceIsNotStuck(t *testing.T) {
	var table, log = watchedTable()
	if _, err := table.begin(6); err != nil {
		t.Fatal(err)
	}
	table.cancel(6)
	time.Sleep(cancelGrace / 2)
	if !table.settle(6) {
		t.Fatal("a stopped job settled as never told")
	}
	expectNeverStuck(t, log)
}

func TestAJobArrivingOutOfOrderIsRefused(t *testing.T) {
	var table, _ = watchedTable()
	if _, err := table.begin(7); err != nil {
		t.Fatal(err)
	}
	if _, err := table.begin(7); err == nil {
		t.Fatal("a repeated job id was accepted")
	}
}

func TestAForgottenBuildIsNeverStuck(t *testing.T) {
	var table, log = watchedTable()
	table.forget(9)
	ctx, err := table.begin(9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.begin(10); err != nil {
		t.Fatal(err)
	}
	table.forget(10)
	if ctx.Err() == nil {
		t.Fatal("a build forgotten before it arrived started running")
	}
	time.Sleep(2 * cancelGrace)
	if stuck := log.noted(); len(stuck) != 0 {
		t.Fatalf("forgotten builds %v were judged stuck", stuck)
	}
	if table.keep(9, builtUnit(t)) || table.keep(10, builtUnit(t)) {
		t.Fatal("a forgotten build was kept")
	}
	expectResident(t, table, 0)
}

func TestACancelAfterAForgetStartsTheGrace(t *testing.T) {
	var table, log = watchedTable()
	if _, err := table.begin(11); err != nil {
		t.Fatal(err)
	}
	table.forget(11)
	table.cancel(11)
	time.Sleep(cancelGrace + 100*time.Millisecond)
	if stuck := log.noted(); len(stuck) != 1 || stuck[0] != 11 {
		t.Fatalf("judged stuck: %v, want only job 11", stuck)
	}
}
