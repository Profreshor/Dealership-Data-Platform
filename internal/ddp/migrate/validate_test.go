package migrate

import (
	"testing"
	"testing/fstest"
)

func TestValidateMigrationNames(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"bad calendar timestamp": {"ddp/20261304000000_init.sql": &fstest.MapFile{}},
		"duplicate timestamp": {
			"ddp/20260101000000_a.sql": &fstest.MapFile{},
			"ddp/20260101000000_b.sql": &fstest.MapFile{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validate(files); err == nil {
				t.Fatal("validate succeeded")
			}
		})
	}
}
