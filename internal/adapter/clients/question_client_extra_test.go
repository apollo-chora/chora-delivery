// question_client_extra_test.go — tops up SnapshotQuestion branches not
// exercised by question_client_test.go: the nil-receiver / nil-rpc guards,
// the UNIMPLEMENTED fail-loud case, and the nil-response guard.
package clients

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	creationv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/creation/v1"

	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func TestQuestionClient_SnapshotQuestion_NilReceiver(t *testing.T) {
	t.Parallel()
	var c *QuestionClient
	if _, err := c.SnapshotQuestion(context.Background(), "tenant", "q1", "gcid"); err == nil {
		t.Fatal("nil receiver: want error")
	}
}

func TestQuestionClient_SnapshotQuestion_NilRPC(t *testing.T) {
	t.Parallel()
	c := NewQuestionClient(nil)
	if _, err := c.SnapshotQuestion(context.Background(), "tenant", "q1", "gcid"); err == nil {
		t.Fatal("nil rpc: want error")
	}
}

func TestQuestionClient_SnapshotQuestion_UnimplementedFailsLoud(t *testing.T) {
	t.Parallel()
	c := NewQuestionClient(&fakeCreationGRPCClient{err: status.Error(codes.Unimplemented, "reserved type")})
	_, err := c.SnapshotQuestion(context.Background(), "tenant", "q1", "gcid")
	if err == nil || err == domain.ErrQuestionSnapshotNotFound {
		t.Fatalf("UNIMPLEMENTED should fail loud with a wrapped error, got %v", err)
	}
}

func TestQuestionClient_SnapshotQuestion_NilResponse(t *testing.T) {
	t.Parallel()
	fake := &fakeCreationGRPCClient{payloads: map[string]*creationv1.SnapshotQuestionByIDResponse{"q1": nil}}
	c := NewQuestionClient(fake)
	if _, err := c.SnapshotQuestion(context.Background(), "tenant", "q1", "gcid"); err == nil {
		t.Fatal("nil response: want error")
	}
}
