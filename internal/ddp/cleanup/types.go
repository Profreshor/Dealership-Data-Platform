// Package cleanup expires platform payloads while retaining operational identities.
package cleanup

type Counts struct {
	RunLogs        int64 `json:"run_logs"`
	Messages       int64 `json:"messages"`
	HealthDeleted  int64 `json:"health_deleted"`
	HealthExpired  int64 `json:"health_expired"`
	AlertContexts  int64 `json:"alert_contexts"`
	ModelErrors    int64 `json:"model_errors"`
	Sessions       int64 `json:"sessions"`
	PasswordTokens int64 `json:"password_tokens"`
	More           bool  `json:"more"`
}
