package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
)

// Backup declares public storage and recovery settings; credentials stay in the environment.
type Backup struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region" jsonschema:"minLength=1"`
	Bucket    string `json:"bucket" jsonschema:"minLength=3,maxLength=63"`
	Recipient string `json:"recipient" jsonschema:"minLength=1"`
	Interval  string `json:"interval,omitempty"`
	Retention string `json:"retention,omitempty"`
}

func (b Backup) Durations() (interval, retention time.Duration) {
	interval, retention = 24*time.Hour, 14*24*time.Hour
	if b.Interval != "" {
		interval, _ = time.ParseDuration(b.Interval)
	}
	if b.Retention != "" {
		retention, _ = time.ParseDuration(b.Retention)
	}
	return
}

func ValidateBackup(b *Backup) error {
	if b == nil {
		return errors.New("deploy.backup is required")
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("deploy.backup.endpoint must be an HTTPS origin")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(b.Bucket) || strings.Contains(b.Bucket, "..") || !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(b.Region) {
		return errors.New("deploy.backup needs a valid bucket and region")
	}
	if _, err := age.ParseX25519Recipient(b.Recipient); err != nil {
		return errors.New("deploy.backup.recipient must be an age X25519 public key")
	}
	interval, retention := b.Durations()
	if interval < time.Minute || retention < interval || retention > 3650*24*time.Hour {
		return errors.New("backup durations must be valid: interval at least 1m, retention at least interval and at most 10 years")
	}
	return nil
}
