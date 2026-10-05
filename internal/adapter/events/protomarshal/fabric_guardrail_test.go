// fabric_guardrail_test — the Flag-1 CI guardrail from the 2026-07-01
// event-fabric audit. It is the durable regression gate that WOULD HAVE CAUGHT
// the kg_hexagon_fog class of bug: a topic that chora-delivery PRODUCES and
// that is bound to a BINARY Pub/Sub Schema Registry schema, but for which
// protomarshal has NO encoder case — so the outbox silently JSON-falls-back and
// the binary schema rejects every publish → dead-letter forever.
//
// Data sources (deployed-reality precedence — CLAUDE.md source hierarchy #1):
//
//   - "bound to a binary schema" = a delivery topic in the m10-data-plane
//     terraform `local.pubsub_topics` map MINUS `local.schemaless_topics`. That
//     terraform IS what provisions the Schema Registry bindings that make binary
//     encoding mandatory, so it is the authoritative source — NOT
//     chora-infra/topics/topics.yaml, which is a stale partial catalogue that
//     (a) lists every delivery topic with a `schema:` field including ~35
//     declared-only scaffolding topics that legitimately JSON-fall-back, and
//     (b) does not even contain the assessment / submission / grading-lifecycle
//     topics that the audit found broken. topics.yaml is still cross-checked
//     below (TestTopicsYaml_CataloguesFabricRepairTopics) for the two Class-D
//     topics this repair added.
//
//   - "chora-delivery produces it" = the topic string literal appears in the
//     service's Go source OUTSIDE the encoder (protomarshal), the decoder
//     (protodecode) and the subscribers (consume-only surfaces) and the tests.
//     This scopes the assertion to emitted topics so it does not false-fail on
//     the declared-only scaffolding (booking.created / exam.* / rostering.* /
//     …) that has a schema but no producer + no encoder yet.
//
// NOTE (per the audit): this is a CI TEST only. encodeOutboxPayload MUST NOT be
// made runtime-fail-loud — 57 topics platform-wide legitimately publish
// schemaless JSON, and a runtime blanket fail-loud would break them.
package protomarshal_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events/protomarshal"
)

// deliveryTopicRe matches a fully-qualified delivery topic that is a STANDALONE
// double-quoted string literal (Go source string constant, or a TF map key /
// list element). Requiring the surrounding quotes is what distinguishes a real
// producer literal from a topic merely mentioned in a // comment or embedded in
// a larger log-format string (e.g. a subscriber logging the topic it consumes —
// chora.delivery.grading.oe_batch_completed.v1 is subscribed to, not produced).
var deliveryTopicRe = regexp.MustCompile(`"(chora\.delivery\.[a-z0-9_.]+\.v1)"`)

// topicLiterals returns the set of delivery topics quoted as standalone string
// literals in s (capture group 1 of each match).
func topicLiterals(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range deliveryTopicRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// repoRoot walks up from this test's source file (stable at build time,
// independent of the test's working directory) to the monorepo root — the
// directory holding both chora-infra/ and services/chora-delivery/.
//
// In the standalone, cloud-neutral chora-delivery repository the monorepo
// layout no longer exists: chora-infra (the terraform that provisioned the
// Schema Registry bindings) was removed with the cloud dependencies. The
// guardrail therefore SKIPS rather than fails when the authoritative source is
// absent — it cannot assert binary-schema coverage without it. A local
// (NATS-only) deployment has no binary Schema Registry, so the dead-letter
// class this gate guards against does not apply.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 15; i++ {
		infra := filepath.Join(dir, "chora-infra", "terraform", "modules", "m10-data-plane", "main.tf")
		svc := filepath.Join(dir, "services", "chora-delivery")
		if fileExists(infra) && dirExists(svc) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skipf("chora-infra monorepo root not present (standalone cloud-neutral repo); "+
		"the binary Schema Registry guardrail has no authoritative source walking up from %s", file)
	return ""
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// deliveryBinaryBoundTopics returns the set of delivery topics bound to a
// binary Schema Registry schema = every delivery topic literal in the
// m10-data-plane terraform MINUS the ones in local.schemaless_topics.
func deliveryBinaryBoundTopics(t *testing.T, root string) map[string]bool {
	t.Helper()
	tfPath := filepath.Join(root, "chora-infra", "terraform", "modules", "m10-data-plane", "main.tf")
	raw, err := os.ReadFile(tfPath)
	if err != nil {
		t.Fatalf("read m10-data-plane main.tf: %v", err)
	}
	tf := string(raw)

	all := topicLiterals(tf)

	// Extract the schemaless_topics = toset([ ... ]) block and remove its
	// delivery members — those intentionally publish schemaless JSON.
	schemaless := map[string]bool{}
	if start := strings.Index(tf, "schemaless_topics = toset(["); start >= 0 {
		rest := tf[start:]
		if end := strings.Index(rest, "])"); end >= 0 {
			schemaless = topicLiterals(rest[:end])
		} else {
			t.Fatal("schemaless_topics block has no closing '])' — TF parse assumption broken")
		}
	} else {
		t.Fatal("could not locate local.schemaless_topics in main.tf — TF parse assumption broken")
	}

	bound := map[string]bool{}
	for topic := range all {
		if !schemaless[topic] {
			bound[topic] = true
		}
	}
	if len(bound) == 0 {
		t.Fatal("0 binary-bound delivery topics parsed from TF — parse is broken (would make the guardrail vacuously green)")
	}
	return bound
}

// producedDeliveryTopics scans chora-delivery's Go source for delivery topic
// string literals, EXCLUDING the encoder (protomarshal — the code under test),
// the decoder (protodecode) and the subscribers (consume-only surfaces) and
// test files. What remains is the set of topics the service actually emits.
func producedDeliveryTopics(t *testing.T, root string) map[string]bool {
	t.Helper()
	svcRoot := filepath.Join(root, "services", "chora-delivery")
	produced := map[string]bool{}
	err := filepath.WalkDir(svcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Consume-only + self surfaces do not count as producers.
		if strings.Contains(path, string(filepath.Separator)+"protomarshal"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+"protodecode"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+"subscribers"+string(filepath.Separator)) {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for topic := range topicLiterals(string(raw)) {
			produced[topic] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk chora-delivery source: %v", err)
	}
	if len(produced) == 0 {
		t.Fatal("0 produced delivery topics found — source scan is broken (would make the guardrail vacuously green)")
	}
	return produced
}

// TestFabricGuardrail_EveryProducedBinaryTopicHasEncoder is the Flag-1 gate.
// For every delivery topic that chora-delivery PRODUCES and that is bound to a
// binary Schema Registry schema, MarshalPayload MUST route to a real encoder
// (not ErrUnsupportedTopic → JSON fallback → schema-reject → dead-letter).
func TestFabricGuardrail_EveryProducedBinaryTopicHasEncoder(t *testing.T) {
	root := repoRoot(t)
	binaryBound := deliveryBinaryBoundTopics(t, root)
	produced := producedDeliveryTopics(t, root)
	env := fixedEnvelope()
	// A minimal payload with the common id keys the encoders reach for; absent
	// fields are proto3-default-omitted, so this is enough to prove routing.
	minimal := map[string]any{
		"course_id":     "c",
		"assessment_id": "a",
		"submission_id": "s",
		"oe_batch_id":   "b",
	}

	checked := 0
	for topic := range binaryBound {
		if !produced[topic] {
			// Declared-only scaffolding (schema provisioned, no producer yet).
			t.Logf("declared-only (binary schema, no chora-delivery producer yet): %s", topic)
			continue
		}
		_, err := protomarshal.MarshalPayload(topic, env, minimal)
		if protomarshal.IsUnsupportedTopic(err) {
			t.Errorf("topic %q is PRODUCED by chora-delivery and bound to a binary Pub/Sub "+
				"schema, but protomarshal has NO encoder case → JSON fallback → Schema "+
				"Registry rejects → dead-letter (kg_hexagon_fog class). Add a MarshalPayload case.", topic)
			continue
		}
		if err != nil {
			t.Errorf("topic %q: MarshalPayload returned a non-routing error: %v", topic, err)
			continue
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("guardrail asserted 0 produced binary-bound topics — the TF parse or the source scan is broken")
	}
	t.Logf("Flag-1 guardrail: %d produced binary-bound delivery topics all have encoders", checked)
}

// TestTopicsYaml_CataloguesFabricRepairTopics honours the audit directive to
// parse chora-infra/topics/topics.yaml: it asserts the two Class-D topics this
// repair added are catalogued there with their per-event schema names (the
// authoritative binary-encoder coverage is proven by the test above).
func TestTopicsYaml_CataloguesFabricRepairTopics(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "chora-infra", "topics", "topics.yaml"))
	if err != nil {
		t.Fatalf("read topics.yaml: %v", err)
	}
	yaml := string(raw)
	for _, want := range []struct{ topic, schema string }{
		{"chora.delivery.course.published.v1", "chora-delivery-course-published-v1"},
		{"chora.delivery.grading.mcq_snapshot_missing.v1", "chora-delivery-grading-mcq_snapshot_missing-v1"},
	} {
		if !strings.Contains(yaml, want.topic) {
			t.Errorf("topics.yaml missing topic %q", want.topic)
		}
		if !strings.Contains(yaml, want.schema) {
			t.Errorf("topics.yaml missing schema %q for %q", want.schema, want.topic)
		}
	}
}
