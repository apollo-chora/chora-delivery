// pii_closure_map_extra_test.go — tops up the loader error paths +
// AGID-applicability inference not covered by pii_closure_map_test.go.
package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPIIClosureMap_FromFile_Missing(t *testing.T) {
	if _, err := LoadFromFile(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("missing file: want error")
	}
}

func TestLoadPIIClosureMap_BadYAML(t *testing.T) {
	if _, err := LoadFromBytes([]byte(": : : nope {{")); err == nil {
		t.Fatal("malformed YAML: want error")
	}
}

func TestLoadPIIClosureMap_AGIDApplicable_Inference(t *testing.T) {
	base := `
domain: %s
fields_to_tokenize:
  - table: t
    columns:
      - column: c
        strategy: drop
`
	for _, tc := range []struct {
		domain string
		want   bool
	}{
		{"chora_a2a", true},
		{"chora_delivery", false},
	} {
		tc := tc
		t.Run(tc.domain, func(t *testing.T) {
			t.Parallel()
			m, err := LoadFromBytes([]byte(strings.Replace(base, "%s", tc.domain, 1)))
			if err != nil {
				t.Fatalf("LoadFromBytes: %v", err)
			}
			if m.AGIDApplicable != tc.want {
				t.Fatalf("AGIDApplicable(%q) = %v, want %v", tc.domain, m.AGIDApplicable, tc.want)
			}
		})
	}
}
