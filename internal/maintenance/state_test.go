package maintenance

import (
	"testing"
	"time"
)

var testStart = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)

func newTestCard(t *testing.T, kind Kind, limit float64) Card {
	t.Helper()
	card, err := NewCard("test-card", kind, "测试部件", limit, testStart)
	if err != nil {
		t.Fatalf("new card: %v", err)
	}
	return card
}

func TestAccrueCrossesLimitOnce(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindPressUnit, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	crossed, err := state.Accrue("test-card", 60, "feed_web", testStart.Add(time.Hour))
	if err != nil || crossed {
		t.Fatalf("first accrue crossed=%v err=%v", crossed, err)
	}
	crossed, err = state.Accrue("test-card", 50, "feed_web", testStart.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("second accrue: %v", err)
	}
	if !crossed {
		t.Fatal("expected crossing at 110/100")
	}
	crossed, err = state.Accrue("test-card", 10, "feed_web", testStart.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("third accrue: %v", err)
	}
	if crossed {
		t.Fatal("overdue must only be reported once per cycle")
	}
	card, _ := state.Card("test-card")
	if len(card.Overdues) != 1 {
		t.Fatalf("expected 1 overdue event, got %d", len(card.Overdues))
	}
	if card.Overdues[0].Usage != 110 || card.Overdues[0].Limit != 100 {
		t.Fatalf("overdue event mismatch: %+v", card.Overdues[0])
	}
}

func TestAccrueRejectsNegative(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindPressUnit, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := state.Accrue("test-card", -5, "cheat", testStart); err == nil {
		t.Fatal("negative accrual must be rejected")
	}
}

func TestBackfillRules(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindUnwindBrake, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	now := testStart.Add(24 * time.Hour)
	if _, err := state.Backfill("test-card", 0, testStart, "zero", now); err == nil {
		t.Fatal("zero backfill must be rejected")
	}
	if _, err := state.Backfill("test-card", -10, testStart, "negative", now); err == nil {
		t.Fatal("negative backfill must be rejected")
	}
	if _, err := state.Backfill("test-card", 10, now.Add(time.Hour), "future", now); err == nil {
		t.Fatal("future effective time must be rejected")
	}
	crossed, err := state.Backfill("test-card", 150, testStart.Add(time.Hour), "历史补录", now)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !crossed {
		t.Fatal("backfill over the limit must report a crossing")
	}
	card, _ := state.Card("test-card")
	if len(card.Overdues) != 1 {
		t.Fatalf("expected 1 overdue event, got %d", len(card.Overdues))
	}
	event := card.Overdues[0]
	if !event.EffectiveAt.Equal(testStart.Add(time.Hour)) {
		t.Fatalf("overdue effective time should be the backfill time, got %v", event.EffectiveAt)
	}
	if !event.DetectedAt.Equal(now) {
		t.Fatalf("overdue detection time should be now, got %v", event.DetectedAt)
	}
}

func TestBackfillNeverErasesOverdueHistory(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindPressUnit, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	now := testStart.Add(24 * time.Hour)
	if _, err := state.Backfill("test-card", 120, testStart, "第一次补录", now); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, err := state.Replace("test-card", 500, "换新", now.Add(time.Hour)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	card, _ := state.Card("test-card")
	if card.Usage != 0 || card.LifeLimit != 500 {
		t.Fatalf("replacement must reset usage and set new limit: %+v", card)
	}
	if card.Overdue() {
		t.Fatal("fresh component must not be overdue")
	}
	if len(card.Overdues) != 1 {
		t.Fatalf("overdue history must survive replacement, got %d", len(card.Overdues))
	}
	if _, err := state.Backfill("test-card", 30, testStart.Add(2*time.Hour), "补录历史", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	card, _ = state.Card("test-card")
	if len(card.Overdues) != 1 {
		t.Fatalf("backfill must not erase past overdue events, got %d", len(card.Overdues))
	}
	if card.Usage != 30 {
		t.Fatalf("usage after backfill = %v, want 30", card.Usage)
	}
}

func TestServiceRecordDoesNotResetUsage(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindDryerFan, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := state.Accrue("test-card", 80, "fan_runtime", testStart.Add(time.Hour)); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	record, err := state.RecordService("test-card", "更换轴承", testStart.Add(2*time.Hour), testStart.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("record service: %v", err)
	}
	if record.Usage != 80 {
		t.Fatalf("service record should capture current usage 80, got %v", record.Usage)
	}
	card, _ := state.Card("test-card")
	if card.Usage != 80 {
		t.Fatalf("servicing must not reset usage, got %v", card.Usage)
	}
	if len(card.Records) != 1 {
		t.Fatalf("expected 1 service record, got %d", len(card.Records))
	}
}

func TestReplaceValidatesLimit(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindPressUnit, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := state.Replace("test-card", 0, "bad", testStart); err == nil {
		t.Fatal("zero new limit must be rejected")
	}
	if _, err := state.Replace("missing", 100, "bad", testStart); err == nil {
		t.Fatal("unknown component must be rejected")
	}
}

func TestFanRunningHours(t *testing.T) {
	state := NewState()
	if err := state.Register(newTestCard(t, KindDryerFan, 100)); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := state.SetRunning("test-card", true, testStart); err != nil {
		t.Fatalf("set running: %v", err)
	}
	settlements, err := state.Tick(testStart.Add(10 * time.Hour))
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(settlements) != 1 || settlements[0].Units != 10 {
		t.Fatalf("expected 10 settled hours, got %+v", settlements)
	}
	card, _ := state.Card("test-card")
	if !card.Running {
		t.Fatal("fan should still be running after tick")
	}
	settled, err := state.SetRunning("test-card", false, testStart.Add(15*time.Hour))
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if settled != 5 {
		t.Fatalf("expected 5 more settled hours, got %v", settled)
	}
	card, _ = state.Card("test-card")
	if card.Usage != 15 || card.Running {
		t.Fatalf("usage=%v running=%v, want 15/false", card.Usage, card.Running)
	}
}

func TestForecastWindow(t *testing.T) {
	state := NewState()
	card := newTestCard(t, KindPressUnit, 1000)
	if err := state.Register(card); err != nil {
		t.Fatalf("register: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := state.Accrue("test-card", 50, "feed_web", testStart.Add(time.Duration(i)*24*time.Hour)); err != nil {
			t.Fatalf("accrue: %v", err)
		}
	}
	card, _ = state.Card("test-card")
	window := ForecastWindow(card, testStart.Add(4*24*time.Hour))
	if window.Confidence != "projected" {
		t.Fatalf("confidence = %q, want projected", window.Confidence)
	}
	if window.RatePerDay != 50 {
		t.Fatalf("rate = %v, want 50/day", window.RatePerDay)
	}
	wantDue := testStart.Add(4 * 24 * time.Hour).Add(16 * 24 * time.Hour)
	if !window.PredictedDue.Equal(wantDue) {
		t.Fatalf("predicted due = %v, want %v", window.PredictedDue, wantDue)
	}
	if !window.WindowEnd.Equal(wantDue) || !window.WindowStart.Equal(wantDue.Add(-24*time.Hour)) {
		t.Fatalf("window = %v..%v", window.WindowStart, window.WindowEnd)
	}
}

func TestForecastInsufficientData(t *testing.T) {
	card := newTestCard(t, KindPressUnit, 1000)
	window := ForecastWindow(card, testStart.Add(24*time.Hour))
	if window.Confidence != "insufficient_data" {
		t.Fatalf("confidence = %q, want insufficient_data", window.Confidence)
	}
	if !window.PredictedDue.IsZero() {
		t.Fatal("no due date should be projected without data")
	}
}

func TestForecastOverdue(t *testing.T) {
	card := newTestCard(t, KindPressUnit, 100)
	card.Usage = 150
	window := ForecastWindow(card, testStart)
	if !window.Overdue || window.Confidence != "overdue" {
		t.Fatalf("window = %+v", window)
	}
}
