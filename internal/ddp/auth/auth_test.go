package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashAndFloodGuardAreBounded(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") || !VerifyPassword(hash, "correct horse battery staple") || VerifyPassword(hash, "wrong password") {
		t.Fatal("Argon2id password verification failed")
	}
	bad := []string{
		"$argon2id$v=19$m=999999999,t=3,p=4$c2FsdHNhbHQ$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=4garbage$c2FsdHNhbHQ$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		strings.Repeat("x", 513),
	}
	for _, encoded := range bad {
		if VerifyPassword(encoded, "password") {
			t.Fatalf("accepted invalid password hash %q", encoded)
		}
	}

	s := New(nil, 0)
	for i := 0; i < 5000; i++ {
		if !s.allowed(fmt.Sprintf("user-%d@example.test", i)) {
			t.Fatal("first attempt was rate limited")
		}
	}
	if len(s.attempts) > 4096 {
		t.Fatalf("flood guard retained %d identities", len(s.attempts))
	}
	for i := 0; i < 10; i++ {
		if !s.allowed("repeated@example.test") {
			t.Fatalf("attempt %d was rejected early", i+1)
		}
	}
	if s.allowed("repeated@example.test") {
		t.Fatal("eleventh attempt was accepted")
	}
}

// A nil database makes any accidental query on these rejected paths fail the test.
func TestLoginRejectsInvalidAndLimitedInputBeforeDatabase(t *testing.T) {
	s := New(nil, 0)
	for _, input := range []LoginRequest{
		{Email: strings.Repeat("x", 255) + "@example.test", Password: "password"},
		{Email: "valid@example.test", Password: strings.Repeat("x", 129)},
		{Email: "valid@example.test", Password: "short"},
	} {
		if _, _, err := s.Login(context.Background(), input.Email, input.Password); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("invalid input: %v", err)
		}
	}
	if len(s.attempts) != 0 {
		t.Fatal("invalid input retained in limiter")
	}
	now := time.Now()
	s.now = func() time.Time { return now }
	for range 10 {
		s.allowed("valid@example.test")
	}
	if _, _, err := s.Login(context.Background(), " VALID@example.test ", "password"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("limited input: %v", err)
	}
	now = now.Add(time.Minute)
	if !s.allowed("valid@example.test") {
		t.Fatal("login budget did not recover")
	}
}
