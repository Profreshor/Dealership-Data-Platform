package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUsersInviteContract(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Execute(t.Context(), []string{"help", "users", "invite", "--json"}, &out, &errOut, nil); code != 0 {
		t.Fatalf("help: %d %s %s", code, out.String(), errOut.String())
	}
	var result struct {
		Data struct {
			Name         string `json:"name"`
			RequiredRole string `json:"required_role"`
			Audit        string `json:"audit"`
			Flags        []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"flags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Name != "ddp users invite" || result.Data.RequiredRole != "administrator" || result.Data.Audit != "transactional: users.invite" {
		t.Fatalf("unexpected invite declaration: %+v", result.Data)
	}
	seen := map[string]string{}
	for _, flag := range result.Data.Flags {
		seen[flag.Name] = flag.Type
	}
	if seen["email"] != "string" || seen["role"] != "stringArray" {
		t.Fatalf("invite flags = %#v", seen)
	}

	out.Reset()
	errOut.Reset()
	if code := Execute(t.Context(), []string{"users", "invite", "--json"}, &out, &errOut, nil); code != 2 {
		t.Fatalf("missing email code = %d, output = %s", code, out.String())
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "usage" || errOut.Len() != 0 {
		t.Fatalf("unexpected usage response: %s %s", out.String(), errOut.String())
	}
}

func TestAccountAdministrationCommandContracts(t *testing.T) {
	for _, command := range []struct {
		args   []string
		action string
	}{
		{[]string{"users", "list"}, "none"},
		{[]string{"users", "update"}, "transactional: users.update"},
		{[]string{"users", "disable"}, "transactional: users.disable"},
		{[]string{"users", "roles", "save"}, "transactional: roles.save"},
	} {
		var out, errOut bytes.Buffer
		args := append([]string{"help"}, command.args...)
		args = append(args, "--json")
		if code := Execute(t.Context(), args, &out, &errOut, nil); code != 0 {
			t.Fatalf("help: %d %s", code, &out)
		}
		var envelope struct {
			Data struct {
				RequiredRole string `json:"required_role"`
				Audit        string
			}
		}
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Data.RequiredRole != "administrator" || envelope.Data.Audit != command.action {
			t.Fatalf("policy: %s", &out)
		}
	}
	for _, args := range [][]string{
		{"users", "disable"},
		{"users", "disable", "one", "two"},
		{"users", "update", "person"},
		{"users", "update", "person", "--disabled=false"},
		{"users", "update", "person", "--disabled=false", "--role", "reader", "--clear-roles"},
		{"users", "roles", "save", "reader", "--name", "Reader"},
		{"users", "roles", "save", "reader", "--name", "Reader", "--permission", "users.manage", "--clear-permissions"},
	} {
		var out, errOut bytes.Buffer
		if code := Execute(t.Context(), append(args, "--json"), &out, &errOut, nil); code != 2 {
			t.Fatalf("unsafe missing/ambiguous flags %v: %d %s", args, code, &out)
		}
	}
}
