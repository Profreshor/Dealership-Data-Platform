package config

import (
	"testing"
	"time"

	"filippo.io/age"
)

func TestBackupSettings(t *testing.T) {
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	base := Backup{Endpoint: "https://storage.example.test", Region: "auto", Bucket: "client-backups", Recipient: key.Recipient().String()}
	if err := ValidateBackup(&base); err != nil {
		t.Fatal(err)
	}
	if interval, retention := base.Durations(); interval != 24*time.Hour || retention != 336*time.Hour {
		t.Fatal("wrong defaults")
	}
	for _, mutate := range []func(*Backup){
		func(b *Backup) { b.Endpoint = "http://storage.example.test" },
		func(b *Backup) { b.Endpoint = "https://secret@storage.example.test" },
		func(b *Backup) { b.Endpoint = "https://storage.example.test/?secret" },
		func(b *Backup) { b.Endpoint = "https://storage.example.test/path" },
		func(b *Backup) { b.Recipient = "invalid" },
		func(b *Backup) { b.Bucket = "../other" },
		func(b *Backup) { b.Interval = "invalid" },
		func(b *Backup) { b.Retention = "1h" },
	} {
		b := base
		mutate(&b)
		if err := ValidateBackup(&b); err == nil {
			t.Fatalf("accepted invalid backup: %+v", b)
		}
	}
}
