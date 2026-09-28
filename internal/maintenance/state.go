package maintenance

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

type State struct {
	mu    sync.RWMutex
	cards map[string]Card
}

func NewState() *State {
	return &State{cards: map[string]Card{}}
}

func (state *State) Register(card Card) error {
	if card.ID == "" {
		return fmt.Errorf("maintenance component identity is incomplete")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, exists := state.cards[card.ID]; exists {
		return fmt.Errorf("maintenance component %q already exists", card.ID)
	}
	state.cards[card.ID] = card
	return nil
}

// Accrue adds metered usage produced by machine operation.
// It returns true when this accrual pushes the component newly past its life limit.
func (state *State) Accrue(id string, units float64, source string, now time.Time) (bool, error) {
	if units < 0 {
		return false, fmt.Errorf("maintenance usage cannot be negative")
	}
	if units == 0 {
		return false, nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	card, ok := state.cards[id]
	if !ok {
		return false, fmt.Errorf("maintenance component %q is not registered", id)
	}
	wasOverdue := card.Overdue()
	card.addUsage(units, source, now)
	newlyOverdue := false
	if card.Overdue() && !wasOverdue {
		card.markOverdue(source, now, now)
		newlyOverdue = true
	}
	state.cards[id] = card
	return newlyOverdue, nil
}

// Backfill records historical usage that was never metered at runtime.
// Only positive increments are accepted, so a backfill can never erase usage.
// Crossing the limit stamps an overdue event at the supplied effective time;
// overdue history is append-only and survives replacement of the component.
func (state *State) Backfill(id string, units float64, effectiveAt time.Time, reason string, now time.Time) (bool, error) {
	if units <= 0 {
		return false, fmt.Errorf("backfilled maintenance usage must be positive")
	}
	if effectiveAt.IsZero() {
		return false, fmt.Errorf("backfill effective time is required")
	}
	if effectiveAt.After(now) {
		return false, fmt.Errorf("backfill effective time cannot be in the future")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	card, ok := state.cards[id]
	if !ok {
		return false, fmt.Errorf("maintenance component %q is not registered", id)
	}
	wasOverdue := card.Overdue()
	card.addUsage(units, "backfill: "+reason, effectiveAt)
	newlyOverdue := false
	if card.Overdue() && !wasOverdue {
		card.markOverdue("backfill: "+reason, now, effectiveAt)
		newlyOverdue = true
	}
	state.cards[id] = card
	return newlyOverdue, nil
}

// RecordService logs an inspection or repair. Servicing never clears usage
// and never clears overdue history; only Replace resets the cycle.
func (state *State) RecordService(id, note string, performedAt time.Time, now time.Time) (ServiceRecord, error) {
	if note == "" {
		return ServiceRecord{}, fmt.Errorf("maintenance service note is required")
	}
	if performedAt.IsZero() {
		performedAt = now
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	card, ok := state.cards[id]
	if !ok {
		return ServiceRecord{}, fmt.Errorf("maintenance component %q is not registered", id)
	}
	record := ServiceRecord{Note: note, Usage: card.Usage, PerformedAt: performedAt.UTC(), RecordedAt: now.UTC()}
	card.Records = append(card.Records, record)
	state.cards[id] = card
	return record, nil
}

// Replace installs a fresh component. The new cycle starts at zero usage with
// a new life limit, but the full overdue history of previous cycles is retained.
func (state *State) Replace(id string, newLimit float64, note string, now time.Time) (Replacement, error) {
	if newLimit <= 0 {
		return Replacement{}, fmt.Errorf("replacement life limit must be positive")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	card, ok := state.cards[id]
	if !ok {
		return Replacement{}, fmt.Errorf("maintenance component %q is not registered", id)
	}
	replacement := Replacement{Note: note, OldLimit: card.LifeLimit, NewLimit: newLimit, FinalUsage: card.Usage, ReplacedAt: now.UTC()}
	card.Replacements = append(card.Replacements, replacement)
	card.LifeLimit = newLimit
	card.Usage = 0
	card.Running = false
	card.RunningSince = time.Time{}
	card.UsageLog = nil
	card.InstalledAt = now.UTC()
	state.cards[id] = card
	return replacement, nil
}

// Settlement reports running hours settled for one fan during a Tick or stop.
type Settlement struct {
	ID           string
	Units        float64
	NewlyOverdue bool
}

// SetRunning marks a fan-style component running or stopped. Stopping settles
// the elapsed hours and returns them (zero when the fan was not running).
func (state *State) SetRunning(id string, running bool, now time.Time) (float64, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	card, ok := state.cards[id]
	if !ok {
		return 0, fmt.Errorf("maintenance component %q is not registered", id)
	}
	if card.Kind != KindDryerFan {
		return 0, fmt.Errorf("component %q does not accrue running hours", id)
	}
	if running == card.Running {
		return 0, nil
	}
	settled := 0.0
	if running {
		card.Running = true
		card.RunningSince = now.UTC()
	} else {
		units, err := card.settle(now, "fan_runtime")
		if err != nil {
			return 0, err
		}
		settled = units
		card.Running = false
		card.RunningSince = time.Time{}
	}
	state.cards[id] = card
	return settled, nil
}

// Tick settles elapsed running hours for every running fan; fans keep running
// with their clock restarted at now.
func (state *State) Tick(now time.Time) ([]Settlement, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	var settlements []Settlement
	for id, card := range state.cards {
		if card.Kind != KindDryerFan || !card.Running {
			continue
		}
		wasOverdue := card.Overdue()
		units, err := card.settle(now, "fan_runtime")
		if err != nil {
			return nil, err
		}
		card.RunningSince = now.UTC()
		state.cards[id] = card
		settlements = append(settlements, Settlement{ID: id, Units: units, NewlyOverdue: card.Overdue() && !wasOverdue})
	}
	sort.Slice(settlements, func(i, j int) bool { return settlements[i].ID < settlements[j].ID })
	return settlements, nil
}

// settle adds the hours elapsed since RunningSince to usage. The caller
// decides whether the fan keeps running.
func (card *Card) settle(now time.Time, source string) (float64, error) {
	if !card.Running || card.RunningSince.IsZero() {
		return 0, nil
	}
	elapsed := now.Sub(card.RunningSince)
	if elapsed < 0 {
		return 0, fmt.Errorf("maintenance clock moved backwards for component %q", card.ID)
	}
	hours := elapsed.Hours()
	if hours <= 0 {
		return 0, nil
	}
	wasOverdue := card.Overdue()
	card.addUsage(hours, source, now)
	if card.Overdue() && !wasOverdue {
		card.markOverdue(source, now, now)
	}
	return hours, nil
}

func (state *State) Overdue() []Card {
	state.mu.RLock()
	defer state.mu.RUnlock()
	var overdue []Card
	for _, card := range state.cards {
		if card.Overdue() {
			overdue = append(overdue, card.copy())
		}
	}
	sort.Slice(overdue, func(i, j int) bool { return overdue[i].ID < overdue[j].ID })
	return overdue
}

func (state *State) Card(id string) (Card, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	card, ok := state.cards[id]
	if !ok {
		return Card{}, false
	}
	return card.copy(), true
}

func (state *State) Cards() []Card {
	state.mu.RLock()
	defer state.mu.RUnlock()
	cards := make([]Card, 0, len(state.cards))
	for _, card := range state.cards {
		cards = append(cards, card.copy())
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards
}

// Restore replaces the whole projection; used when rebuilding from the journal.
func (state *State) Restore(cards []Card) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.cards = map[string]Card{}
	for _, card := range cards {
		state.cards[card.ID] = card
	}
}
