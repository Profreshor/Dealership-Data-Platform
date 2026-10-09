package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestInitRequiresDiscoveryBeforeOnboard(t *testing.T) {
	var out bytes.Buffer
	command := initCommand(commandCatalog{}, func(value any) error { out.WriteString("called"); return nil })
	command.SetArgs([]string{"/tmp/client"})
	if err := command.ExecuteContext(context.Background()); err == nil || out.Len() != 0 {
		t.Fatalf("missing discovery: err=%v output=%q", err, out.String())
	}
}

func TestInitExposesLocalTemplateAndDryRunFlags(t *testing.T) {
	command := initCommand(commandCatalog{}, func(any) error { return nil })
	for _, name := range []string{"discovery", "template", "dry-run"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("missing --%s", name)
		}
	}
}

func TestInitExecuteReturnsMachineErrorWithoutWriting(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"init", t.TempDir(), "--discovery", filepath.Join(t.TempDir(), "missing.yaml"), "--json"}, &out, &errOut, nil)
	if code == 0 || out.Len() == 0 || errOut.Len() != 0 {
		t.Fatalf("init code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	var envelope struct{ OK bool }
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.OK {
		t.Fatalf("invalid discovery envelope=%q err=%v", out.String(), err)
	}
}
