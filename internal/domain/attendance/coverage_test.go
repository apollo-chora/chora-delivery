// coverage_test.go — closes the remaining statement gaps in qr_token.go:
// Issue's blank-field guards, Validate's malformed/signature/base64/field-count/
// timestamp rejection branches, and parseInt64's non-numeric path. Tokens are
// crafted by replicating the documented format (base64url(payload) + "." +
// hex(hmac-sha256(payload, key))) so each malformation is signed correctly and
// reaches exactly the branch under test.
package attendance_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/domain/attendance"
)

// signedToken builds a syntactically well-formed token over an arbitrary raw
// payload-bearing base64url segment, signed with the test key. It lets a test
// feed a VALID signature past hmac.Equal so the failure happens at the payload
// layer (base64 / field count / timestamp), exactly like a buggy issuer would.
func signedToken(encPayload string) string {
	mac := hmac.New(sha256.New, []byte(hmacKey1))
	mac.Write([]byte(encPayload))
	return encPayload + "." + hex.EncodeToString(mac.Sum(nil))
}

func TestIssue_RejectsBlankFields(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	cases := []struct {
		name     string
		tenant   string
		class    string
		wantAnon error
	}{
		{"blank tenant", "   ", classA, attendance.ErrInvalidArgument},
		{"blank class", tenantA, " \t", attendance.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := signer.Issue(c.tenant, c.class, now); !errors.Is(err, c.wantAnon) {
				t.Errorf("Issue(%q,%q): got %v want %v", c.tenant, c.class, err, c.wantAnon)
			}
		})
	}
}

func TestValidate_MalformedTokenRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	for _, tok := range []string{"", "no-dot-here", "a.b.c"} {
		// "a.b.c" splits into 2 parts (payload "a", sig "b.c") — a valid shape that
		// then fails hex/signature, so this case asserts ErrTokenInvalid generally.
		if _, err := signer.Validate(tok, now); !errors.Is(err, attendance.ErrTokenInvalid) {
			t.Errorf("Validate(%q): got %v want ErrTokenInvalid", tok, err)
		}
	}
}

func TestValidate_InvalidHexSignatureRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	tok, _ := signer.Issue(tenantA, classA, now)
	parts := fmt.Sprintf("%s.%s", mustSplit(t, tok.Token)[0], "zz-not-hex")
	if _, err := signer.Validate(parts, now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("non-hex sig: got %v want ErrTokenInvalid", err)
	}
}

func TestValidate_BadPayloadEncodingRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	// "!!!" is not valid base64url; the signature is computed over it, so the
	// malformed-encoding branch is reached (not the signature branch).
	if _, err := signer.Validate(signedToken("!!!"), now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("bad base64 payload: got %v want ErrTokenInvalid", err)
	}
}

func TestValidate_WrongFieldCountRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	payload := base64.RawURLEncoding.EncodeToString([]byte("t|class|123")) // 3 fields, not 4
	if _, err := signer.Validate(signedToken(payload), now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("3-field payload: got %v want ErrTokenInvalid", err)
	}
}

func TestValidate_BadTimestampRejected(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	// parseInt64 hits its non-numeric-character branch → "bad timestamp".
	payload := base64.RawURLEncoding.EncodeToString([]byte(tenantA + "|" + classA + "|12x|deadbeef"))
	if _, err := signer.Validate(signedToken(payload), now); !errors.Is(err, attendance.ErrTokenInvalid) {
		t.Errorf("non-numeric timestamp: got %v want ErrTokenInvalid", err)
	}
}

func TestValidate_HappyClaimsRoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	signer, _ := attendance.NewTokenSigner([]byte(hmacKey1))
	tok, _ := signer.Issue(tenantA, classA, now)
	claims, err := signer.Validate(tok.Token, now)
	if err != nil {
		t.Fatalf("Validate happy: %v", err)
	}
	if claims.Nonce == "" {
		t.Errorf("nonce must be carried back")
	}
}

// mustSplit returns token.Split(".") with a test guard.
func mustSplit(t *testing.T, token string) []string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("token shape: %q", token)
	}
	return parts
}
