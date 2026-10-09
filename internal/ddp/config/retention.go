package config

// Retention controls platform evidence; client data and audit identities are not purged.
type Retention struct {
	RunLogs  string `json:"run_logs,omitempty"`
	Health   string `json:"health,omitempty"`
	Messages string `json:"messages,omitempty"`
}

func (c *Config) RetentionPolicy() Retention {
	policy := Retention{RunLogs: "720h", Health: "2160h", Messages: "720h"}
	if c.Retention != nil {
		if c.Retention.RunLogs != "" {
			policy.RunLogs = c.Retention.RunLogs
		}
		if c.Retention.Health != "" {
			policy.Health = c.Retention.Health
		}
		if c.Retention.Messages != "" {
			policy.Messages = c.Retention.Messages
		}
	}
	return policy
}
