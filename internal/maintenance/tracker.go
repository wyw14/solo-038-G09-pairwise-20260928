package maintenance

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// futureSkew tolerates small clock offsets on incoming readings.
const futureSkew = time.Minute

// SourceBackfill marks readings that were recorded after the fact.
const SourceBackfill = "backfill"

// ServiceKind classifies a maintenance service record.
type ServiceKind string

const (
	ServiceInspection  ServiceKind = "inspection"
	ServiceReplacement ServiceKind = "replacement"
)

// UsageRecord is one append-only metering reading for a component.
// ObservedAt is when the usage actually accrued (may be historical for
// backfills); RecordedAt is when the reading entered the tracker.
type UsageRecord struct {
	ID         string        `json:"id"`
	Component  ComponentKind `json:"component"`
	Segment    uint64        `json:"segment"`
	Delta      float64       `json:"delta"`
	Unit       string        `json:"unit"`
	Source     string        `json:"source"`
	ObservedAt time.Time     `json:"observed_at"`
	RecordedAt time.Time     `json:"recorded_at"`
}

// ServiceRecord is one append-only maintenance action. An inspection
// certifies the part for one more lifespan and extends the overdue
// threshold; a replacement closes the current segment and opens a fresh
// one with a new lifespan.
type ServiceRecord struct {
	ID        string        `json:"id"`
	Component ComponentKind `json:"component"`
	Segment   uint64        `json:"segment"`
	Kind      ServiceKind   `json:"kind"`
	NewLimit  float64       `json:"new_limit,omitempty"`
	Note      string        `json:"note,omitempty"`
	At        time.Time     `json:"at"`
}

// OverdueLatch is projected from the append-only timeline: it exists when
// cumulative segment usage crossed the effective threshold before a
// qualifying service action. Latches are never deleted or rewritten, so a
// backfilled historical reading still surfaces the overdue period it
// caused. Resolution is derived from later inspections or from the
// replacement that closed the segment.
type OverdueLatch struct {
	ID         string        `json:"id"`
	Component  ComponentKind `json:"component"`
	Segment    uint64        `json:"segment"`
	Level      int           `json:"level"`
	Threshold  float64       `json:"threshold"`
	CrossedAt  time.Time     `json:"crossed_at"`
	DetectedAt time.Time     `json:"detected_at"`
}

// LatchView pairs a projected latch with its derived resolution state.
type LatchView struct {
	OverdueLatch
	Resolved   bool       `json:"resolved"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy string     `json:"resolved_by,omitempty"`
}

// Window is the predicted maintenance window for one component.
type Window struct {
	Component      ComponentKind `json:"component"`
	Status         string        `json:"status"`
	Remaining      float64       `json:"remaining"`
	Unit           string        `json:"unit"`
	RatePerDay     float64       `json:"rate_per_day"`
	EstimatedDueAt *time.Time    `json:"estimated_due_at,omitempty"`
	SuggestedStart time.Time     `json:"suggested_start"`
	SuggestedEnd   time.Time     `json:"suggested_end"`
}

// Card is the operator-facing view of one component's maintenance state.
type Card struct {
	Component         ComponentKind   `json:"component"`
	Unit              string          `json:"unit"`
	Segment           uint64          `json:"segment"`
	Usage             float64         `json:"usage"`
	Limit             float64         `json:"limit"`
	Threshold         float64         `json:"threshold"`
	Remaining         float64         `json:"remaining"`
	Status            string          `json:"status"`
	UnresolvedOverdue int             `json:"unresolved_overdue"`
	Latches           []LatchView     `json:"latches"`
	Services          []ServiceRecord `json:"services"`
	Window            *Window         `json:"window,omitempty"`
}

// projection is the materialized maintenance state derived from a
// component's append-only reading and service timeline.
type projection struct {
	segment          uint64
	limit            float64
	threshold        float64
	usage            float64
	status           string
	inspections      int
	currentReadings  []UsageRecord
	latches          []LatchView
	services         []ServiceRecord
	pending          *LatchView
	segmentStartedAt time.Time
}

type componentState struct {
	spec     ComponentSpec
	readings []UsageRecord
	services []ServiceRecord
	view     projection
}

// segmentAtLocked returns the segment generation active at the given time:
// one plus the number of replacements performed at or before that time.
// Historical backfills are attributed to the segment they accrued in.
func (state *componentState) segmentAtLocked(at time.Time) uint64 {
	var segment uint64 = 1
	for _, service := range state.services {
		if service.Kind == ServiceReplacement && !at.Before(service.At) {
			segment++
		}
	}
	return segment
}

// Tracker accumulates component usage, projects overdue latches, and
// derives maintenance windows. All mutations are journaled (append-only)
// before state is re-projected.
type Tracker struct {
	mu    sync.Mutex
	store *Store
	now   func() time.Time
	comps map[ComponentKind]*componentState
	order []ComponentKind
}

// NewTracker builds an empty tracker. store may be nil for in-memory use;
// now may be nil to use the wall clock.
func NewTracker(store *Store, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	tracker := &Tracker{store: store, now: now, comps: map[ComponentKind]*componentState{}}
	for _, spec := range Specs() {
		tracker.comps[spec.Kind] = &componentState{spec: spec}
		tracker.order = append(tracker.order, spec.Kind)
	}
	tracker.reprojectLocked()
	return tracker
}

// OpenTracker loads the append-only journal and projects tracker state
// from it. Nothing but the journal is needed to reconstruct every latch,
// including overdue periods that only surface via later backfills.
func OpenTracker(path string) (*Tracker, error) {
	store := NewStore(path)
	records, err := store.Load()
	if err != nil {
		return nil, err
	}
	tracker := NewTracker(store, nil)
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	for _, record := range records {
		switch record.Type {
		case recordUsage:
			tracker.ingestLocked(*record.Usage)
		case recordService:
			tracker.ingestLockedService(*record.Service)
		}
	}
	tracker.reprojectLocked()
	return tracker, nil
}

func (tracker *Tracker) ingestLocked(record UsageRecord) {
	state := tracker.comps[record.Component]
	state.readings = append(state.readings, record)
}

func (tracker *Tracker) ingestLockedService(record ServiceRecord) {
	state := tracker.comps[record.Component]
	state.services = append(state.services, record)
}

// RecordUsage meters actual usage against a component. When the reading
// pushes its segment past the effective overdue threshold, a new latch is
// projected for the segment.
func (tracker *Tracker) RecordUsage(component ComponentKind, delta float64, observedAt time.Time, source string) (UsageRecord, error) {
	spec, err := SpecFor(component)
	if err != nil {
		return UsageRecord{}, err
	}
	if delta <= 0 {
		return UsageRecord{}, fmt.Errorf("usage delta must be positive")
	}
	if observedAt.IsZero() {
		return UsageRecord{}, fmt.Errorf("usage observed time is required")
	}
	now := tracker.now().UTC()
	observedAt = observedAt.UTC()
	if observedAt.After(now.Add(futureSkew)) {
		return UsageRecord{}, fmt.Errorf("usage observed time is in the future")
	}
	if source == "" {
		return UsageRecord{}, fmt.Errorf("usage source is required")
	}
	tracker.mu.Lock()
	segment := tracker.comps[component].segmentAtLocked(observedAt)
	record := UsageRecord{ID: uuid.NewString(), Component: component, Segment: segment, Delta: delta, Unit: spec.Unit, Source: source, ObservedAt: observedAt, RecordedAt: now}
	tracker.mu.Unlock()
	if tracker.store != nil {
		if err := tracker.store.Append(storedRecord{Type: recordUsage, Usage: &record}); err != nil {
			return UsageRecord{}, err
		}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.ingestLocked(record)
	tracker.reprojectLocked()
	return record, nil
}

// Backfill records historical usage. The reading is attributed to the
// segment that was active at ObservedAt; re-projecting the timeline can
// only ever add overdue latches for that period, never remove one.
func (tracker *Tracker) Backfill(component ComponentKind, delta float64, observedAt time.Time) (UsageRecord, error) {
	return tracker.RecordUsage(component, delta, observedAt, SourceBackfill)
}

// RecordService journals an inspection or replacement. An inspection
// certifies the current part for one more lifespan (resolving its
// outstanding latch and extending the threshold); a replacement installs
// a fresh part with the given lifespan (or the component default).
func (tracker *Tracker) RecordService(component ComponentKind, kind ServiceKind, newLimit float64, note string) (ServiceRecord, error) {
	if _, err := SpecFor(component); err != nil {
		return ServiceRecord{}, err
	}
	if kind != ServiceInspection && kind != ServiceReplacement {
		return ServiceRecord{}, fmt.Errorf("service kind %q is invalid", kind)
	}
	if newLimit < 0 {
		return ServiceRecord{}, fmt.Errorf("new limit must not be negative")
	}
	now := tracker.now().UTC()
	tracker.mu.Lock()
	segment := tracker.comps[component].view.segment
	record := ServiceRecord{ID: uuid.NewString(), Component: component, Segment: segment, Kind: kind, NewLimit: newLimit, Note: note, At: now}
	tracker.mu.Unlock()
	if tracker.store != nil {
		if err := tracker.store.Append(storedRecord{Type: recordService, Service: &record}); err != nil {
			return ServiceRecord{}, err
		}
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.ingestLockedService(record)
	tracker.reprojectLocked()
	return record, nil
}

type timedEvent struct {
	at      time.Time
	ordinal int // readings (0) sort before services (1) at identical times
	reading *UsageRecord
	service *ServiceRecord
}

// reprojectLocked rebuilds every component's latch timeline from its raw
// append-only records. The walk merges readings (by ObservedAt) and
// services (by At); each inspection extends the effective threshold by one
// lifespan and settles the outstanding latch, and each replacement closes
// the segment (settling any outstanding latch) before opening the next.
func (tracker *Tracker) reprojectLocked() {
	for _, kind := range tracker.order {
		tracker.comps[kind].view = tracker.projectLocked(tracker.comps[kind])
	}
}

func (tracker *Tracker) projectLocked(state *componentState) projection {
	events := make([]timedEvent, 0, len(state.readings)+len(state.services))
	for index := range state.readings {
		events = append(events, timedEvent{at: state.readings[index].ObservedAt, ordinal: 0, reading: &state.readings[index]})
	}
	for index := range state.services {
		events = append(events, timedEvent{at: state.services[index].At, ordinal: 1, service: &state.services[index]})
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].at.Equal(events[right].at) {
			return events[left].ordinal < events[right].ordinal
		}
		return events[left].at.Before(events[right].at)
	})

	view := projection{segment: 1, limit: state.spec.DefaultLimit, threshold: state.spec.DefaultLimit, latches: []LatchView{}, services: append([]ServiceRecord(nil), state.services...), currentReadings: []UsageRecord{}}
	var usage float64
	var inspections int
	closeSegment := func(at time.Time, by string) {
		if view.pending != nil {
			view.pending.Resolved = true
			resolvedAt := at
			view.pending.ResolvedAt = &resolvedAt
			view.pending.ResolvedBy = by
			view.pending = nil
		}
	}
	addLatch := func(level int, threshold float64, crossedAt, detectedAt time.Time) {
		latch := LatchView{OverdueLatch: OverdueLatch{
			ID:         fmt.Sprintf("%s-s%d-l%d", state.spec.Kind, view.segment, level),
			Component:  state.spec.Kind,
			Segment:    view.segment,
			Level:      level,
			Threshold:  threshold,
			CrossedAt:  crossedAt,
			DetectedAt: detectedAt,
		}}
		view.latches = append(view.latches, latch)
		view.pending = &view.latches[len(view.latches)-1]
	}
	for _, event := range events {
		switch {
		case event.reading != nil:
			usage += event.reading.Delta
			if view.pending == nil && usage > float64(1+inspections)*view.limit {
				addLatch(1+inspections, float64(1+inspections)*view.limit, event.reading.ObservedAt, event.reading.RecordedAt)
			}
		case event.service != nil && event.service.Kind == ServiceInspection:
			closeSegment(event.service.At, "inspection")
			inspections++
			// A part that is already past the renewed threshold latches again immediately.
			if usage > float64(1+inspections)*view.limit {
				addLatch(1+inspections, float64(1+inspections)*view.limit, event.service.At, event.service.At)
			}
		case event.service != nil && event.service.Kind == ServiceReplacement:
			closeSegment(event.service.At, "replacement")
			view.segment++
			limit := event.service.NewLimit
			if limit <= 0 {
				limit = state.spec.DefaultLimit
			}
			view.limit = limit
			usage = 0
			inspections = 0
			view.segmentStartedAt = event.service.At
		}
	}
	view.usage = usage
	view.inspections = inspections
	view.threshold = float64(1+inspections) * view.limit
	if view.pending != nil {
		view.status = StatusOverdue
	} else if view.usage >= dueSoonFraction*view.threshold {
		view.status = StatusDueSoon
	} else {
		view.status = StatusOK
	}
	for _, reading := range state.readings {
		if !reading.ObservedAt.Before(view.segmentStartedAt) {
			view.currentReadings = append(view.currentReadings, reading)
		}
	}
	sort.Slice(view.services, func(left, right int) bool { return view.services[left].At.Before(view.services[right].At) })
	sort.Slice(view.latches, func(left, right int) bool {
		if view.latches[left].CrossedAt.Equal(view.latches[right].CrossedAt) {
			return view.latches[left].Level < view.latches[right].Level
		}
		return view.latches[left].CrossedAt.Before(view.latches[right].CrossedAt)
	})
	return view
}

// Blocked lists components with an unresolved overdue latch, in stable
// component order. New jobs must not start while this is non-empty.
func (tracker *Tracker) Blocked() []ComponentKind {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	blocked := []ComponentKind{}
	for _, kind := range tracker.order {
		if tracker.comps[kind].view.pending != nil {
			blocked = append(blocked, kind)
		}
	}
	return blocked
}

// IsBlocked reports whether a single component currently blocks startups.
func (tracker *Tracker) IsBlocked(component ComponentKind) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state, ok := tracker.comps[component]
	return ok && state.view.pending != nil
}

// Card renders the maintenance card for one component.
func (tracker *Tracker) Card(component ComponentKind) (Card, error) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	state, ok := tracker.comps[component]
	if !ok {
		return Card{}, fmt.Errorf("%w: %q", ErrUnknownComponent, component)
	}
	return tracker.cardLocked(state, tracker.now().UTC()), nil
}

// Cards renders every component's maintenance card in stable order.
func (tracker *Tracker) Cards() []Card {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.now().UTC()
	cards := make([]Card, 0, len(tracker.order))
	for _, kind := range tracker.order {
		cards = append(cards, tracker.cardLocked(tracker.comps[kind], now))
	}
	return cards
}

func (tracker *Tracker) cardLocked(state *componentState, now time.Time) Card {
	view := state.view
	remaining := view.threshold - view.usage
	if remaining < 0 {
		remaining = 0
	}
	card := Card{
		Component: state.spec.Kind,
		Unit:      state.spec.Unit,
		Segment:   view.segment,
		Usage:     view.usage,
		Limit:     view.limit,
		Threshold: view.threshold,
		Remaining: remaining,
		Status:    view.status,
		Latches:   append([]LatchView(nil), view.latches...),
		Services:  append([]ServiceRecord(nil), view.services...),
		Window:    tracker.windowLocked(state, now),
	}
	for _, latch := range card.Latches {
		if !latch.Resolved {
			card.UnresolvedOverdue++
		}
	}
	return card
}

// ratePerDay estimates the current segment's usage rate as the segment's
// total metered usage averaged from its first reading until now. Backfilled
// readings widen the observation span, so the estimate is a long-run
// average rather than an instantaneous rate.
func ratePerDay(readings []UsageRecord, now time.Time) float64 {
	if len(readings) < 2 {
		return 0
	}
	earliest := now
	total := 0.0
	for _, reading := range readings {
		if reading.ObservedAt.Before(earliest) {
			earliest = reading.ObservedAt
		}
		total += reading.Delta
	}
	span := now.Sub(earliest)
	if span < time.Hour {
		return 0
	}
	return total / span.Hours() * 24
}

func (tracker *Tracker) windowLocked(state *componentState, now time.Time) *Window {
	view := state.view
	remaining := view.threshold - view.usage
	if remaining < 0 {
		remaining = 0
	}
	window := &Window{Component: state.spec.Kind, Status: view.status, Remaining: remaining, Unit: state.spec.Unit}
	if view.pending != nil {
		due := now
		window.EstimatedDueAt = &due
		window.SuggestedStart = now
		window.SuggestedEnd = now.Add(24 * time.Hour)
		return window
	}
	rate := ratePerDay(view.currentReadings, now)
	if rate <= 0 {
		return nil
	}
	window.RatePerDay = rate
	due := now.Add(time.Duration(remaining/rate*24) * time.Hour)
	window.EstimatedDueAt = &due
	start := due.Add(-24 * time.Hour)
	if start.Before(now) {
		start = now
	}
	window.SuggestedStart = start
	window.SuggestedEnd = due
	return window
}

// Windows lists the predicted maintenance window of every component that
// has enough data (or is already overdue), soonest first.
func (tracker *Tracker) Windows() []Window {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	now := tracker.now().UTC()
	windows := []Window{}
	for _, kind := range tracker.order {
		if window := tracker.windowLocked(tracker.comps[kind], now); window != nil {
			windows = append(windows, *window)
		}
	}
	sort.Slice(windows, func(left, right int) bool { return windows[left].SuggestedStart.Before(windows[right].SuggestedStart) })
	return windows
}
