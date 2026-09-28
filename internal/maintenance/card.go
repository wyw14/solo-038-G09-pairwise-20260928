package maintenance

import (
	"fmt"
	"time"
)

type Kind string

const (
	KindPressUnit   Kind = "press_unit"
	KindDryerFan    Kind = "dryer_fan"
	KindUnwindBrake Kind = "unwind_brake"
)

var kindUnits = map[Kind]string{
	KindPressUnit:   "meters",
	KindDryerFan:    "hours",
	KindUnwindBrake: "cycles",
}

type ServiceRecord struct {
	Note        string    `json:"note"`
	Usage       float64   `json:"usage"`
	PerformedAt time.Time `json:"performed_at"`
	RecordedAt  time.Time `json:"recorded_at"`
}

type Replacement struct {
	Note       string    `json:"note"`
	OldLimit   float64   `json:"old_limit"`
	NewLimit   float64   `json:"new_limit"`
	FinalUsage float64   `json:"final_usage"`
	ReplacedAt time.Time `json:"replaced_at"`
}

type OverdueEvent struct {
	Usage       float64   `json:"usage"`
	Limit       float64   `json:"limit"`
	Source      string    `json:"source"`
	DetectedAt  time.Time `json:"detected_at"`
	EffectiveAt time.Time `json:"effective_at"`
}

type UsageEntry struct {
	Units  float64   `json:"units"`
	Source string    `json:"source"`
	At     time.Time `json:"at"`
}

type Card struct {
	ID           string          `json:"id"`
	Kind         Kind            `json:"kind"`
	Name         string          `json:"name"`
	Unit         string          `json:"unit"`
	LifeLimit    float64         `json:"life_limit"`
	Usage        float64         `json:"usage"`
	InstalledAt  time.Time       `json:"installed_at"`
	Running      bool            `json:"running"`
	RunningSince time.Time       `json:"running_since,omitempty"`
	Records      []ServiceRecord `json:"records"`
	Replacements []Replacement   `json:"replacements"`
	Overdues     []OverdueEvent  `json:"overdues"`
	UsageLog     []UsageEntry    `json:"usage_log"`
}

const usageLogLimit = 200

func NewCard(id string, kind Kind, name string, lifeLimit float64, now time.Time) (Card, error) {
	unit, ok := kindUnits[kind]
	if !ok {
		return Card{}, fmt.Errorf("maintenance component kind %q is unsupported", kind)
	}
	if id == "" || name == "" {
		return Card{}, fmt.Errorf("maintenance component identity is incomplete")
	}
	if lifeLimit <= 0 {
		return Card{}, fmt.Errorf("maintenance life limit must be positive")
	}
	return Card{ID: id, Kind: kind, Name: name, Unit: unit, LifeLimit: lifeLimit, InstalledAt: now.UTC()}, nil
}

func (card Card) Overdue() bool {
	return card.Usage >= card.LifeLimit
}

func (card Card) Remaining() float64 {
	remaining := card.LifeLimit - card.Usage
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (card *Card) addUsage(units float64, source string, at time.Time) {
	card.Usage += units
	card.UsageLog = append(card.UsageLog, UsageEntry{Units: units, Source: source, At: at.UTC()})
	if len(card.UsageLog) > usageLogLimit {
		card.UsageLog = card.UsageLog[len(card.UsageLog)-usageLogLimit:]
	}
}

func (card *Card) markOverdue(source string, detectedAt, effectiveAt time.Time) {
	card.Overdues = append(card.Overdues, OverdueEvent{Usage: card.Usage, Limit: card.LifeLimit, Source: source, DetectedAt: detectedAt.UTC(), EffectiveAt: effectiveAt.UTC()})
}

func (card Card) copy() Card {
	copied := card
	copied.Records = append([]ServiceRecord(nil), card.Records...)
	copied.Replacements = append([]Replacement(nil), card.Replacements...)
	copied.Overdues = append([]OverdueEvent(nil), card.Overdues...)
	copied.UsageLog = append([]UsageEntry(nil), card.UsageLog...)
	return copied
}
