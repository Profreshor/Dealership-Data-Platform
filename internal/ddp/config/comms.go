package config

import (
	"errors"
	"net"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
)

var secretName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func ValidEmail(value string) bool {
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && strings.Contains(value, "@")
}

func ValidateSMTP(s *SMTP) error {
	if s == nil {
		return errors.New("comms.smtp is required")
	}
	host, port, err := net.SplitHostPort(s.Addr)
	number, numberErr := strconv.Atoi(port)
	if err != nil || host == "" || numberErr != nil || number < 1 || number > 65535 || strings.ContainsAny(host, " /\r\n\t") {
		return errors.New("comms.smtp.addr must be host:port")
	}
	if !ValidEmail(s.From) {
		return errors.New("comms.smtp.from must be a bare email address")
	}
	if (s.Username == "") != (s.PasswordEnv == "") || len(s.Username) > 254 || strings.ContainsAny(s.Username, "\r\n") {
		return errors.New("SMTP username and password_env must be supplied together")
	}
	if s.PasswordEnv != "" && !secretName.MatchString(s.PasswordEnv) {
		return errors.New("SMTP password_env must name an environment variable")
	}
	switch s.TLS {
	case "starttls", "implicit":
	case "none":
		if s.Username != "" {
			return errors.New("plaintext SMTP cannot authenticate")
		}
	default:
		return errors.New("SMTP tls must be starttls, implicit or none")
	}
	return nil
}
