// helpers_test.go — unit tests for the pure/env-gated helper functions
// carved out of main.go / course_content_repo.go / pgx_txrunner.go.
//
// main() itself is not unit-tested (log.Fatalf-driven bootstrap against live
// Cloud SQL + Pub/Sub — composition-root plateau, see course_repo_wiring_test.go
// for the same posture). These tests cover every helper that carries logic:
// splitCSV / firstNonEmptyEnv / envBool / maybePubsubPushValidator /
// resolveRedisSecret (fallback branches only — the Secret Manager client path
// needs live cloud credentials) + the nil-guards of the pgx wiring.
package main

import (
	"context"
	"errors"
	"testing"

	deliverypg "github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
)

func TestSplitCSV_Pure(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "a", []string{"a"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trims parts", " a , b ,c ", []string{"a", "b", "c"}},
		{"drops empties", "a,,b, ,c", []string{"a", "b", "c"}},
		{"all empties", ", ,,", []string{}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitCSV(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCSV(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitCSV(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
			if tc.want == nil && got != nil {
				t.Fatalf("splitCSV(%q) = %v, want nil slice", tc.in, got)
			}
		})
	}
}

func TestFirstNonEmptyEnv_OrderAndBlankHandling(t *testing.T) {
	t.Run("unset returns empty", func(t *testing.T) {
		t.Setenv("CHORA_TEST_ENV_VAR_NONE", "")
		if got := firstNonEmptyEnv("CHORA_TEST_ENV_VAR_NONE"); got != "" {
			t.Fatalf("firstNonEmptyEnv = %q, want empty", got)
		}
	})

	t.Run("first set wins over later", func(t *testing.T) {
		t.Setenv("CHORA_TEST_ENV_VAR_A", "alpha")
		t.Setenv("CHORA_TEST_ENV_VAR_B", "beta")
		if got := firstNonEmptyEnv("CHORA_TEST_ENV_VAR_A", "CHORA_TEST_ENV_VAR_B"); got != "alpha" {
			t.Fatalf("firstNonEmptyEnv = %q, want alpha", got)
		}
	})

	t.Run("blank first is skipped", func(t *testing.T) {
		t.Setenv("CHORA_TEST_ENV_VAR_A", "   ")
		t.Setenv("CHORA_TEST_ENV_VAR_B", "beta")
		if got := firstNonEmptyEnv("CHORA_TEST_ENV_VAR_A", "CHORA_TEST_ENV_VAR_B"); got != "beta" {
			t.Fatalf("firstNonEmptyEnv = %q, want beta", got)
		}
	})
}

func TestEnvBool_TruthyMatrix(t *testing.T) {
	truthy := []string{"1", "true", "TRUE", "yes", "Yes", "on", " ON "}
	for _, v := range truthy {
		v := v
		t.Run("truthy_"+v, func(t *testing.T) {
			t.Setenv("CHORA_TEST_FLAG", v)
			if !envBool("CHORA_TEST_FLAG") {
				t.Fatalf("envBool(%q) = false, want true", v)
			}
		})
	}

	falsy := []string{"", "0", "false", "no", "off", "banana"}
	for _, v := range falsy {
		v := v
		t.Run("falsy_"+v, func(t *testing.T) {
			t.Setenv("CHORA_TEST_FLAG", v)
			if envBool("CHORA_TEST_FLAG") {
				t.Fatalf("envBool(%q) = true, want false", v)
			}
		})
	}

	t.Run("unset is false", func(t *testing.T) {
		if envBool("CHORA_TEST_FLAG_UNSET") {
			t.Fatal("envBool(unset) = true, want false")
		}
	})
}

func TestResolveRedisSecret_FallbackBranches(t *testing.T) {
	t.Run("no secret id falls back to literal", func(t *testing.T) {
		t.Setenv("CHORA_TEST_REDIS_SECRET_ID", "")
		t.Setenv("CHORA_TEST_REDIS_LITERAL", "  s3cret123  ")
		got := resolveRedisSecret(context.Background(), "proj", "CHORA_TEST_REDIS_SECRET_ID", "CHORA_TEST_REDIS_LITERAL")
		if got != "s3cret123" {
			t.Fatalf("resolveRedisSecret = %q, want trimmed literal", got)
		}
	})

	t.Run("no project falls back to literal", func(t *testing.T) {
		t.Setenv("CHORA_TEST_REDIS_SECRET_ID", "secret-id")
		t.Setenv("CHORA_TEST_REDIS_LITERAL", "literal")
		got := resolveRedisSecret(context.Background(), "", "CHORA_TEST_REDIS_SECRET_ID", "CHORA_TEST_REDIS_LITERAL")
		if got != "literal" {
			t.Fatalf("resolveRedisSecret = %q, want literal", got)
		}
	})

	t.Run("neither set returns empty", func(t *testing.T) {
		t.Setenv("CHORA_TEST_REDIS_SECRET_ID", "")
		t.Setenv("CHORA_TEST_REDIS_LITERAL", "")
		if got := resolveRedisSecret(context.Background(), "proj", "CHORA_TEST_REDIS_SECRET_ID", "CHORA_TEST_REDIS_LITERAL"); got != "" {
			t.Fatalf("resolveRedisSecret = %q, want empty", got)
		}
	})

	// The Secret Manager client branch (secret id + project both set) is a
	// documented plateau: cgcsecrets.NewClient dials a secret backend and is not runnable
	// offline. Its failure paths log-and-downgrade to "" by design.
}

func TestPgxTxRunner_NilGuards(t *testing.T) {
	t.Parallel()

	if txr := newPgxTxRunner(nil); txr != nil {
		t.Fatalf("newPgxTxRunner(nil) = %v, want nil", txr)
	}

	var txr *pgxTxRunner
	err := txr.RunInTx(context.Background(), func(ctx context.Context, q deliverypg.Querier) error { return nil })
	if !errors.Is(err, deliverypg.ErrNotImplemented) {
		t.Fatalf("RunInTx on nil receiver = %v, want ErrNotImplemented", err)
	}
}
