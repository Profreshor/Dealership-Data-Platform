package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryBoundary(t *testing.T) {
	base, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	c.Permissions["customers.read"] = Permission{Description: "Read customer reporting"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"missing section", "jobs: {}\n", ""},
		{"null section", "jobs: {}", "jobs: null"},
		{"unknown section", "jobs: {}", "jobz: {}"},
		{"duplicate section", "jobs: {}", "jobs: {}\njobs: {}"},
		{"bad timezone", "America/Chicago", "not/a/timezone"},
		{"bad workers", "max_workers: 4", "max_workers: 0"},
		{"bad duration", "timeout: 5m", "timeout: -5m"},
		{"bad revision", "template_revision:", "unexpected:"},
		{"extra document", "jobs: {}", "jobs: {}\n---\nfoo: bar"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(string(base), tc.old, tc.replacement, 1))); err == nil {
				t.Fatal("accepted invalid registry")
			}
		})
	}
	c.Jobs["sync"] = Job{Purpose: "Synthetic ingest", Action: "ingest", Python: "jobs.sync", Deletions: "ignore", Reads: []string{"integration/missing"}}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted missing reference")
	}
	c.Jobs["sync"] = Job{Purpose: "First", Action: "check", Python: "jobs.sync", After: []string{"job/second"}}
	c.Jobs["second"] = Job{Purpose: "Second", Action: "check", Python: "jobs.second", After: []string{"job/sync"}}
	if err := c.Validate(); err == nil {
		t.Fatal("accepted cycle")
	}
	delete(c.Jobs, "second")
	c.Jobs["sync"] = Job{Purpose: "First", Action: "check", Python: "jobs.sync"}
	if err := c.ValidateFiles(t.TempDir()); err == nil {
		t.Fatal("accepted missing entrypoint")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "secret.py"), filepath.Join(root, "jobs/sync.py")); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateFiles(root); err == nil {
		t.Fatal("accepted escaping symlink")
	}
}

func TestGeneratedSchema(t *testing.T) {
	want, err := json.MarshalIndent(Schema(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("../../../schema/ddp.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != string(want) {
		t.Fatal("schema drift: run make schema")
	}
}

func TestCommsDeclarationsRejectUnsafeSettings(t *testing.T) {
	base, err := os.ReadFile("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		declaration string
		valid       bool
	}{
		{`{smtp: {addr: 'smtp.example.test:587', from: sender@example.test, tls: starttls}}`, true},
		{`{smtp: {addr: 'smtp.example.test:0', from: sender@example.test, tls: starttls}}`, false},
		{`{smtp: {addr: 'smtp.example.test:587', from: sender@example.test, tls: none, username: user, password_env: SMTP_PASSWORD}}`, false},
		{`{smtp: {addr: 'smtp.example.test:587', from: sender@example.test, tls: starttls, username: user}}`, false},
		{`{smtp: {addr: 'smtp.example.test:587', from: sender@example.test, tls: starttls, username: user, password_env: plaintext-password}}`, false},
		{`{groups: {owners: {recipients: ['Display Name <owner@example.test>']}}}`, false},
	} {
		_, err := Parse([]byte(strings.Replace(string(base), "comms: {}", "comms: "+tc.declaration, 1)))
		if (err == nil) != tc.valid {
			t.Fatalf("valid=%v: %v", tc.valid, err)
		}
	}
}

func TestAccountPagePathsAreReserved(t *testing.T) {
	for _, path := range []string{"/login", "/welcome", "/forgot-password", "/reset-password", "/profile", "/admin/users"} {
		cfg, err := Load("../testdata/base/ddp.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Pages["collision"] = Page{Kind: "custom", Label: "Collision", Path: path}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("path %s: %v", path, err)
		}
	}
}

func TestBrandingBoundary(t *testing.T) {
	cfg, err := Load("../testdata/base/ddp.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, logo := range []string{"https://example.test/logo.svg", "frontend/apps/portal/public/../logo.svg", "frontend/apps/portal/public/a/../../logo.svg", "frontend/apps/portal/public/logo.html", "frontend/apps/portal/public/logo.svg?secret=x"} {
		cfg.Ddp.Branding = &Branding{Logo: logo}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted unsafe branding logo %q", logo)
		}
	}
	cfg.Ddp.Branding = &Branding{Logo: "frontend/apps/portal/public/logo.svg", Accent: "#2457c5"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Ddp.Branding.Accent = "red"
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted malformed accent")
	}
}
