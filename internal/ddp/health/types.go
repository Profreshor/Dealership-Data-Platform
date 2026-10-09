// Package health records operational checks and their notification transitions.
package health

import "time"

// Observation contains bounded, explicit check evidence, never raw database errors.
type Observation struct {
	Ref         string   `json:"ref"`
	Target      string   `json:"target,omitempty"`
	State       string   `json:"state"`
	Severity    string   `json:"severity"`
	Message     string   `json:"message"`
	Value       string   `json:"value,omitempty"`
	ExecutionID string   `json:"execution_id,omitempty"`
	Notify      []string `json:"notify"`
}

type Evaluation struct {
	ID         string    `json:"id"`
	ObservedAt time.Time `json:"observed_at"`
	Observation
}
