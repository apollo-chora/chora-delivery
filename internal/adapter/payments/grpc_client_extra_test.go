// grpc_client_extra_test.go — tops up the pure mapping helpers of the
// payments gRPC client not yet covered by grpc_client_test.go. The
// gRPC surface tests live there; purchaseStateString is a pure enum→name
// map exercised exhaustively here.
package payments

import (
	"testing"

	paymentsv1 "github.com/apollo-chora/chora-contracts/gen/go/chora/services/payments/v1"
)

func TestPurchaseStateString_AllBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state paymentsv1.PurchaseState
		want  string
	}{
		{paymentsv1.PurchaseState_PURCHASE_STATE_CHECKOUT_STARTED, "CHECKOUT_STARTED"},
		{paymentsv1.PurchaseState_PURCHASE_STATE_PAYMENT_CAPTURED, "PAYMENT_CAPTURED"},
		{paymentsv1.PurchaseState_PURCHASE_STATE_PAYMENT_FAILED, "PAYMENT_FAILED"},
		{paymentsv1.PurchaseState_PURCHASE_STATE_REFUNDED, "REFUNDED"},
		{paymentsv1.PurchaseState_PURCHASE_STATE_EXPIRED, "EXPIRED"},
		{paymentsv1.PurchaseState_PURCHASE_STATE_UNSPECIFIED, "UNSPECIFIED"},
		{paymentsv1.PurchaseState(999), "UNSPECIFIED"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := purchaseStateString(tc.state); got != tc.want {
				t.Fatalf("purchaseStateString(%v) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}
}
