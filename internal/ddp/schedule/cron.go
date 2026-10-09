package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

type Occurrence struct {
	At       time.Time
	Local    string
	Timezone string
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func Parse(expression, timezone string) (cron.Schedule, error) {
	loc, err := loadTimezone(timezone)
	if err != nil {
		return nil, err
	}
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		return nil, fmt.Errorf("cron timezone override is not allowed")
	}
	if len(strings.Fields(expression)) != 5 {
		return nil, fmt.Errorf("cron expression must contain exactly five fields")
	}
	s, err := parser.Parse(expression)
	if err != nil {
		return nil, fmt.Errorf("parse cron: %w", err)
	}
	if spec, ok := s.(*cron.SpecSchedule); ok {
		spec.Location = loc
	}
	return s, nil
}

// Between collects occurrences for previews. Planning uses Each to avoid keeping
// an entire outage's ticks in memory.
func Between(expression, timezone string, after, through time.Time) ([]Occurrence, error) {
	var out []Occurrence
	err := Each(expression, timezone, after, through, func(point Occurrence) error {
		out = append(out, point)
		return nil
	})
	return out, err
}

// Each visits occurrences after the exclusive start through the inclusive end.
// Persistent callers must also deduplicate local times across separate calls.
func Each(expression, timezone string, after, through time.Time, visit func(Occurrence) error) error {
	if after.IsZero() || through.IsZero() {
		return fmt.Errorf("after and through must be non-zero")
	}
	if through.Before(after) {
		return fmt.Errorf("through precedes after")
	}
	s, err := Parse(expression, timezone)
	if err != nil {
		return err
	}
	spec := s.(*cron.SpecSchedule)
	loc := spec.Location
	seen := make(map[string]struct{})
	date := ""
	for cursor := after; ; {
		next := spec.Next(cursor)
		if next.IsZero() || next.After(through) {
			return nil
		}
		if !next.After(cursor) {
			return fmt.Errorf("cron schedule did not advance")
		}
		local := next.In(loc).Format("2006-01-02T15:04")
		if local[:10] != date {
			clear(seen)
			date = local[:10]
		}
		if _, exists := seen[local]; !exists {
			seen[local] = struct{}{}
			if err := visit(Occurrence{At: next.UTC(), Local: local, Timezone: timezone}); err != nil {
				return err
			}
		}
		cursor = next
	}
}

func loadTimezone(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("timezone is required")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", name, err)
	}
	return loc, nil
}
