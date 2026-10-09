// Package tui presents the same operational facts and commands as the CLI.
package tui

type Screen string

const (
	Overview       Screen = "Overview"
	Failures       Screen = "Failures"
	Jobs           Screen = "Jobs"
	Runs           Screen = "Runs"
	Logs           Screen = "Logs"
	Integrations   Screen = "Integrations"
	Models         Screen = "Models"
	Tables         Screen = "Tables"
	Health         Screen = "Health"
	Communications Screen = "Comms"
)

var screens = []Screen{Overview, Failures, Jobs, Runs, Logs, Integrations, Models, Tables, Health, Communications}

type Row struct {
	Ref    string
	Label  string
	State  string
	JobRef string
}

type Snapshot struct {
	Summary []string
	Rows    []Row
}
