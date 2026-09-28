package maintenance

import "fmt"

// ComponentKind identifies a maintainable press component.
type ComponentKind string

const (
	ComponentPressUnit   ComponentKind = "press_unit"
	ComponentDryerFan    ComponentKind = "dryer_fan"
	ComponentUnwindBrake ComponentKind = "unwind_brake"
)

// ComponentSpec describes the metering unit and default lifespan of a component.
type ComponentSpec struct {
	Kind         ComponentKind
	Unit         string
	DefaultLimit float64
}

var componentSpecs = []ComponentSpec{
	{Kind: ComponentPressUnit, Unit: "impressions", DefaultLimit: 100000},
	{Kind: ComponentDryerFan, Unit: "minutes", DefaultLimit: 12000},
	{Kind: ComponentUnwindBrake, Unit: "meters", DefaultLimit: 50000},
}

// ErrUnknownComponent is returned when a component kind is not registered.
var ErrUnknownComponent = fmt.Errorf("unknown maintenance component")

// Specs lists every registered component in stable order.
func Specs() []ComponentSpec {
	return append([]ComponentSpec(nil), componentSpecs...)
}

// SpecFor resolves a component kind to its spec.
func SpecFor(kind ComponentKind) (ComponentSpec, error) {
	for _, spec := range componentSpecs {
		if spec.Kind == kind {
			return spec, nil
		}
	}
	return ComponentSpec{}, fmt.Errorf("%w: %q", ErrUnknownComponent, kind)
}

// ParseComponent validates a raw component name.
func ParseComponent(value string) (ComponentKind, error) {
	kind := ComponentKind(value)
	_, err := SpecFor(kind)
	return kind, err
}

// Card status values.
const (
	StatusOK      = "ok"
	StatusDueSoon = "due_soon"
	StatusOverdue = "overdue"
)

// dueSoonFraction of the current threshold at which a card becomes due_soon.
const dueSoonFraction = 0.9
