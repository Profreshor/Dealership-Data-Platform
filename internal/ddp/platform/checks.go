// Package platform reads concrete infrastructure facts shared by health and operators.
package platform

// Result is a bounded observation; errors produce unknown rather than raw diagnostics.
type Result struct {
	State    string `json:"state"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Value    string `json:"value,omitempty"`
}
