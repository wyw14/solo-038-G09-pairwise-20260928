package maintenance

import (
	"fmt"
	"sort"
	"time"

	"github.com/wyw14/solo-038/internal/journal"
	"github.com/wyw14/solo-038/internal/model"
)

const (
	EventCardRegistered    = "maintenance_card_registered"
	EventUsageAccrued      = "maintenance_usage_accrued"
	EventUsageBackfilled   = "maintenance_usage_backfilled"
	EventServiceRecorded   = "maintenance_service_recorded"
	EventComponentReplaced = "maintenance_component_replaced"
)

type registrationPayload struct {
	ID          string    `json:"id"`
	Kind        Kind      `json:"kind"`
	Name        string    `json:"name"`
	Unit        string    `json:"unit"`
	LifeLimit   float64   `json:"life_limit"`
	InstalledAt time.Time `json:"installed_at"`
}

type usagePayload struct {
	CardID string    `json:"card_id"`
	Units  float64   `json:"units"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

type backfillPayload struct {
	CardID      string    `json:"card_id"`
	Units       float64   `json:"units"`
	EffectiveAt time.Time `json:"effective_at"`
	Reason      string    `json:"reason"`
}

type servicePayload struct {
	CardID string        `json:"card_id"`
	Record ServiceRecord `json:"record"`
}

type replacementPayload struct {
	CardID      string      `json:"card_id"`
	Replacement Replacement `json:"replacement"`
}

func PersistRegistration(store *journal.Store, current model.Job, card Card, now time.Time) (uint64, error) {
	payload := registrationPayload{ID: card.ID, Kind: card.Kind, Name: card.Name, Unit: card.Unit, LifeLimit: card.LifeLimit, InstalledAt: card.InstalledAt}
	event, err := model.NewEvent(model.NewOperationID(), current, EventCardRegistered, payload, now)
	if err != nil {
		return 0, err
	}
	return store.Append(event)
}

func PersistUsage(store *journal.Store, current model.Job, cardID string, units float64, source string, at time.Time) (uint64, error) {
	payload := usagePayload{CardID: cardID, Units: units, Source: source, At: at.UTC()}
	event, err := model.NewEvent(model.NewOperationID(), current, EventUsageAccrued, payload, at)
	if err != nil {
		return 0, err
	}
	return store.Append(event)
}

func PersistBackfill(store *journal.Store, current model.Job, cardID string, units float64, effectiveAt time.Time, reason string, now time.Time) (uint64, error) {
	payload := backfillPayload{CardID: cardID, Units: units, EffectiveAt: effectiveAt.UTC(), Reason: reason}
	event, err := model.NewEvent(model.NewOperationID(), current, EventUsageBackfilled, payload, now)
	if err != nil {
		return 0, err
	}
	return store.Append(event)
}

func PersistService(store *journal.Store, current model.Job, cardID string, record ServiceRecord, now time.Time) (uint64, error) {
	event, err := model.NewEvent(model.NewOperationID(), current, EventServiceRecorded, servicePayload{CardID: cardID, Record: record}, now)
	if err != nil {
		return 0, err
	}
	return store.Append(event)
}

func PersistReplacement(store *journal.Store, current model.Job, cardID string, replacement Replacement, now time.Time) (uint64, error) {
	event, err := model.NewEvent(model.NewOperationID(), current, EventComponentReplaced, replacementPayload{CardID: cardID, Replacement: replacement}, now)
	if err != nil {
		return 0, err
	}
	return store.Append(event)
}

// RebuildFromJournal folds maintenance events into a fresh projection.
// Overdue crossings are recomputed from the ordered usage stream, so backfilled
// history produces overdue markers just like live accrual; overdue slices are
// append-only and are never removed by replacement events.
func RebuildFromJournal(store *journal.Store) (*State, error) {
	events, err := store.Events()
	if err != nil {
		return nil, err
	}
	ordered := append([]model.Event(nil), events...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return ordered[left].At.Before(ordered[right].At)
	})
	cards := map[string]Card{}
	require := func(id string) (Card, error) {
		card, ok := cards[id]
		if !ok {
			return Card{}, fmt.Errorf("maintenance event references unregistered component %q", id)
		}
		return card, nil
	}
	for _, event := range ordered {
		if err := event.Validate(); err != nil {
			return nil, err
		}
		switch event.Kind {
		case EventCardRegistered:
			payload, err := model.DecodeEventPayload[registrationPayload](event)
			if err != nil {
				return nil, err
			}
			if _, exists := cards[payload.ID]; exists {
				continue
			}
			cards[payload.ID] = Card{ID: payload.ID, Kind: payload.Kind, Name: payload.Name, Unit: payload.Unit, LifeLimit: payload.LifeLimit, InstalledAt: payload.InstalledAt.UTC()}
		case EventUsageAccrued:
			payload, err := model.DecodeEventPayload[usagePayload](event)
			if err != nil {
				return nil, err
			}
			card, err := require(payload.CardID)
			if err != nil {
				return nil, err
			}
			replayUsage(&card, payload.Units, payload.Source, payload.At, payload.At)
			cards[payload.CardID] = card
		case EventUsageBackfilled:
			payload, err := model.DecodeEventPayload[backfillPayload](event)
			if err != nil {
				return nil, err
			}
			card, err := require(payload.CardID)
			if err != nil {
				return nil, err
			}
			replayUsage(&card, payload.Units, "backfill: "+payload.Reason, payload.EffectiveAt, event.At)
			cards[payload.CardID] = card
		case EventServiceRecorded:
			payload, err := model.DecodeEventPayload[servicePayload](event)
			if err != nil {
				return nil, err
			}
			card, err := require(payload.CardID)
			if err != nil {
				return nil, err
			}
			card.Records = append(card.Records, payload.Record)
			cards[payload.CardID] = card
		case EventComponentReplaced:
			payload, err := model.DecodeEventPayload[replacementPayload](event)
			if err != nil {
				return nil, err
			}
			card, err := require(payload.CardID)
			if err != nil {
				return nil, err
			}
			card.Replacements = append(card.Replacements, payload.Replacement)
			card.LifeLimit = payload.Replacement.NewLimit
			card.Usage = 0
			card.UsageLog = nil
			card.Running = false
			card.RunningSince = time.Time{}
			card.InstalledAt = payload.Replacement.ReplacedAt
			cards[payload.CardID] = card
		}
	}
	state := NewState()
	for _, card := range cards {
		if err := state.Register(card); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func replayUsage(card *Card, units float64, source string, effectiveAt, detectedAt time.Time) {
	if units <= 0 {
		return
	}
	wasOverdue := card.Overdue()
	card.addUsage(units, source, effectiveAt)
	if card.Overdue() && !wasOverdue {
		card.markOverdue(source, detectedAt, effectiveAt)
	}
}
