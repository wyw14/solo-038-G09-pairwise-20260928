package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func testRuntime(t *testing.T) *Runtime {
	t.Helper()
	return testRuntimeWithTolerance(t, 2)
}

func testRuntimeWithTolerance(t *testing.T, tolerance float64) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(Config{DataDir: t.TempDir(), FestoonCapacity: 120, RegisterGap: 500 * time.Millisecond, RegisterTolerance: tolerance})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return runtime
}

func TestOverdueComponentBlocksFeedStartup(t *testing.T) {
	runtime := testRuntime(t)
	ctx := context.Background()
	if err := runtime.ExecuteOperation(ctx, "load_roll"); err != nil {
		t.Fatalf("load_roll: %v", err)
	}
	if _, err := runtime.BackfillMaintenanceUsage(pressUnitCardPrefix+"cyan", 50000, time.Now().Add(-time.Hour), "历史计数补录"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	err := runtime.ExecuteOperation(ctx, "feed_web")
	if err == nil || !strings.Contains(err.Error(), "overdue") {
		t.Fatalf("feed_web must be blocked by overdue component, got: %v", err)
	}
	// The overdue gate must sit ahead of the existing feed interlocks:
	// with the stock tolerance the feed path itself is infeasible, so verify
	// the block using a runtime whose sensor tolerance allows the feed.
	strict := testRuntimeWithTolerance(t, 0.25)
	if err := strict.ExecuteOperation(ctx, "load_roll"); err != nil {
		t.Fatalf("load_roll (strict): %v", err)
	}
	if _, err := strict.BackfillMaintenanceUsage(pressUnitCardPrefix+"cyan", 50000, time.Now().Add(-time.Hour), "历史计数补录"); err != nil {
		t.Fatalf("backfill (strict): %v", err)
	}
	if err := strict.ExecuteOperation(ctx, "feed_web"); err == nil || !strings.Contains(err.Error(), "overdue") {
		t.Fatalf("overdue must block before feed interlocks, got: %v", err)
	}
	if _, err := runtime.ReplaceMaintenanceComponent(pressUnitCardPrefix+"cyan", 50000, "更换印版滚筒轴承"); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := runtime.ExecuteOperation(ctx, "feed_web"); err != nil {
		t.Fatalf("feed_web after replacement must run: %v", err)
	}
}

func TestBackfillOverdueRecordsIncident(t *testing.T) {
	runtime := testRuntime(t)
	if _, err := runtime.BackfillMaintenanceUsage(unwindBrakeCard, 5000, time.Now().Add(-time.Hour), "历史制动次数"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	found := false
	for _, incident := range runtime.Incidents() {
		if incident.Component == "maintenance" && strings.Contains(incident.Message, unwindBrakeCard) {
			found = true
		}
	}
	if !found {
		t.Fatal("overdue backfill must record a maintenance incident")
	}
}

func TestOverdueSurvivesRestartAndReplacement(t *testing.T) {
	dir := t.TempDir()
	runtime, err := NewRuntime(Config{DataDir: dir, FestoonCapacity: 120, RegisterGap: 500 * time.Millisecond, RegisterTolerance: 0.25})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := runtime.BackfillMaintenanceUsage(dryerFanCardEntry, 2000, time.Now().Add(-time.Hour), "历史风机小时"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, err := runtime.ReplaceMaintenanceComponent(dryerFanCardEntry, 2500, "更换风机叶轮"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	restarted, err := NewRuntime(Config{DataDir: dir, FestoonCapacity: 120, RegisterGap: 500 * time.Millisecond, RegisterTolerance: 0.25})
	if err != nil {
		t.Fatalf("second runtime: %v", err)
	}
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	card, ok := restarted.MaintenanceCard(dryerFanCardEntry)
	if !ok {
		t.Fatal("card missing after restart")
	}
	if card.Usage != 0 || card.LifeLimit != 2500 {
		t.Fatalf("cycle must reset with new limit: usage=%v limit=%v", card.Usage, card.LifeLimit)
	}
	if len(card.Overdues) != 1 {
		t.Fatalf("overdue history must survive restart and replacement, got %d", len(card.Overdues))
	}
	if !card.Overdue() && len(restarted.maintenance.Overdue()) != 0 {
		t.Fatal("replaced component must not be overdue after restart")
	}
}

func TestRuntimeAccruesUsageFromOperations(t *testing.T) {
	runtime := testRuntime(t)
	ctx := context.Background()
	if err := runtime.ExecuteOperation(ctx, "load_roll"); err != nil {
		t.Fatalf("load_roll: %v", err)
	}
	if err := runtime.ExecuteOperation(ctx, "feed_web"); err != nil {
		t.Fatalf("feed_web: %v", err)
	}
	brake, _ := runtime.MaintenanceCard(unwindBrakeCard)
	if brake.Usage != 2 {
		t.Fatalf("brake cycles = %v, want 2 (load_roll + feed_web)", brake.Usage)
	}
	for _, unit := range pressUnitNames {
		card, _ := runtime.MaintenanceCard(pressUnitCardPrefix + unit)
		if card.Usage != 1 {
			t.Fatalf("%s usage = %v, want 1 meter", unit, card.Usage)
		}
	}
}

func TestMaintenanceInterlockProjection(t *testing.T) {
	runtime := testRuntime(t)
	clear := false
	for _, interlock := range runtime.Interlocks() {
		if interlock.Name == "maintenance_clear" {
			clear = interlock.Satisfied
		}
	}
	if !clear {
		t.Fatal("maintenance_clear should be satisfied on a fresh runtime")
	}
	if _, err := runtime.BackfillMaintenanceUsage(unwindBrakeCard, 5000, time.Now().Add(-time.Hour), "历史制动次数"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	for _, interlock := range runtime.Interlocks() {
		if interlock.Name == "maintenance_clear" && interlock.Satisfied {
			t.Fatal("maintenance_clear must report unsatisfied when overdue")
		}
	}
}
