package maintenance

import "time"

// Window is the schedulable maintenance forecast for one component.
type Window struct {
	CardID       string    `json:"card_id"`
	Kind         Kind      `json:"kind"`
	Name         string    `json:"name"`
	Unit         string    `json:"unit"`
	Usage        float64   `json:"usage"`
	LifeLimit    float64   `json:"life_limit"`
	Remaining    float64   `json:"remaining"`
	RatePerDay   float64   `json:"rate_per_day"`
	PredictedDue time.Time `json:"predicted_due_at,omitempty"`
	WindowStart  time.Time `json:"window_start,omitempty"`
	WindowEnd    time.Time `json:"window_end,omitempty"`
	Overdue      bool      `json:"overdue"`
	Confidence   string    `json:"confidence"`
}

const (
	// minObservations is the smallest history needed for a real forecast.
	minObservations = 3
	// planningLead is the recommended lead time before the predicted due date.
	planningLead = 24 * time.Hour
)

// ForecastWindow projects a schedulable window for a card at time now.
// Rate is estimated from usage accrued over the current cycle
// (since install or the last replacement).
func ForecastWindow(card Card, now time.Time) Window {
	now = now.UTC()
	window := Window{
		CardID:    card.ID,
		Kind:      card.Kind,
		Name:      card.Name,
		Unit:      card.Unit,
		Usage:     card.Usage,
		LifeLimit: card.LifeLimit,
		Remaining: card.Remaining(),
		Overdue:   card.Overdue(),
	}
	switch card.Kind {
	case KindDryerFan:
		// Fans accrue directly in hours: rate is hours of usage per elapsed day.
		window.RatePerDay = ratePerDay(card.Usage, card.InstalledAt, now, true)
	default:
		window.RatePerDay = ratePerDay(card.Usage, card.InstalledAt, now, false)
	}
	if window.Overdue {
		window.Confidence = "overdue"
		return window
	}
	if len(card.UsageLog) < minObservations || window.RatePerDay <= 0 {
		window.Confidence = "insufficient_data"
		return window
	}
	daysUntilDue := window.Remaining / window.RatePerDay
	due := now.Add(time.Duration(daysUntilDue * float64(24*time.Hour)))
	window.PredictedDue = due
	window.WindowEnd = due
	window.WindowStart = due.Add(-planningLead)
	if window.WindowStart.Before(now) {
		window.WindowStart = now
	}
	window.Confidence = "projected"
	return window
}

// ratePerDay converts accumulated usage over elapsed calendar time into a daily rate.
// For hours-based components the result is usage-hours per day (max 24).
func ratePerDay(usage float64, start, now time.Time, hoursBased bool) float64 {
	if usage <= 0 {
		return 0
	}
	elapsedDays := now.Sub(start.UTC()).Hours() / 24
	if elapsedDays <= 0 {
		return 0
	}
	rate := usage / elapsedDays
	if hoursBased && rate > 24 {
		rate = 24
	}
	return rate
}

func ForecastWindows(cards []Card, now time.Time) []Window {
	windows := make([]Window, 0, len(cards))
	for _, card := range cards {
		windows = append(windows, ForecastWindow(card, now))
	}
	return windows
}
