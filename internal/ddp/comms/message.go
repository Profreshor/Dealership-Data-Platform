// Package comms persists messages before delivering them through SMTP.
package comms

import "regexp"

type Rendered struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

var templateName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validTemplate(name string) bool { return templateName.MatchString(name) }
