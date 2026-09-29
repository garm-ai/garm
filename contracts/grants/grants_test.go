package grants_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/contracts/grant"
	"github.com/garm-ai/garm/contracts/grants"
)

// The material a person was shown when they approved. Every case below either
// presents these values or changes one of them.
func approvedMaterial() map[string]string {
	return map[string]string{
		"amount_minor_units": "1250",
		"currency_code":      "GBP",
		"beneficiary_iban":   "GB29NWBK60161331926819",
	}
}

const (
	theTask    = "tsk_d19f4c"
	theSubject = "customer:C-8123"
	theTool    = "payments.v1.initiate_payment"
)

// signer is a generated key pair and the JWKS that publishes it — the shape a
// real STS hands out, built here so no test needs a fixture token that would
// expire.
type signer struct {
	key  *ecdsa.PrivateKey
	kid  string
	jwks []byte
}

func newSigner(t *testing.T) *signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// A distinct kid per signer, so "signed by a key the set does not
	// publish" is a MISSING key rather than a present one whose signature
	// happens not to check out. The two are different failures and the test
	// that cares asserts the sentinel.
	kid := "sts-" + t.Name() + "-" + strconv.Itoa(nextKid())
	pub := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: key.Public(), KeyID: kid, Algorithm: string(jose.ES256), Use: "sig",
	}}}
	doc, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{key: key, kid: kid, jwks: doc}
}

var kidSeq atomic.Int64

func nextKid() int { return int(kidSeq.Add(1)) }

func (s *signer) keys(t *testing.T) grants.KeySource {
	t.Helper()
	ks, err := grants.ParseJWKS(s.jwks)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

// mint builds a grant. body is applied to the claim map so one case can change
// exactly one thing and the diff in the test reads as the difference.
func (s *signer) mint(t *testing.T, edit func(claims, garmGrant map[string]any)) string {
	t.Helper()
	now := time.Now()
	gg := map[string]any{
		"tool":                  theTool,
		"subject":               theSubject,
		"task":                  theTask,
		"material":              grant.Digest(approvedMaterial()),
		"approver":              "amir@example.test",
		"approver_clearance":    "RESTRICTED",
		"approver_compartments": []any{"financial"},
	}
	claims := map[string]any{
		"iss": "https://sts.example.test",
		"aud": []any{"garmd-eu"},
		"jti": "gr_01J9",
		"iat": float64(now.Unix()),
		"exp": float64(now.Add(15 * time.Minute).Unix()),
	}
	if edit != nil {
		edit(claims, gg)
	}
	claims["garm_grant"] = gg

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", s.kid),
	)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := sig.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A good grant verifies and every check passes. The positive case is first
// because every refusal below is only meaningful against it.
func TestAGoodGrantPassesEveryCheck(t *testing.T) {
	s := newSigner(t)
	c, err := grants.Verify(s.mint(t, nil), s.keys(t))
	if err != nil {
		t.Fatalf("a well-formed grant did not verify: %v", err)
	}

	if c.Task != theTask {
		t.Errorf("task = %q, want %q: the claim the daemon ignores is the one a tasks "+
			"service must read", c.Task, theTask)
	}
	if c.ID == "" {
		t.Error("no jti: a grant that cannot be named cannot be made single-use")
	}
	for name, err := range map[string]error{
		"no act":   c.CheckNoAct(),
		"tool":     c.CheckTool(theTool),
		"subject":  c.CheckSubject(theSubject),
		"task":     c.CheckTask(theTask),
		"fresh":    c.CheckFresh(time.Now(), 30*time.Second, 15*time.Minute),
		"approver": c.CheckApprover(toolv1.Clearance_CLEARANCE_RESTRICTED, []string{"financial"}),
		"material": c.CheckMaterial(approvedMaterial()),
	} {
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The four refusals a tasks service has to make, each over a grant that is
// otherwise perfectly good and correctly signed. That is the point: none of
// these is caught by the signature.
func TestTheRefusals(t *testing.T) {
	s := newSigner(t)
	keys := s.keys(t)

	t.Run("a grant given on another task", func(t *testing.T) {
		// Two tasks over identical material digest identically, so without
		// the task claim a person approving one payment would authorise the
		// other. This is the whole reason the claim exists.
		c := verify(t, s.mint(t, func(_, gg map[string]any) { gg["task"] = "tsk_someone_else" }), keys)
		if err := c.CheckTask(theTask); err == nil {
			t.Fatal("a grant given on another task was accepted")
		} else if !strings.Contains(err.Error(), "tsk_someone_else") {
			t.Errorf("the refusal does not name the task it was given on: %v", err)
		}
		// And every other check still passes, so the task claim is doing the
		// work alone.
		if err := c.CheckMaterial(approvedMaterial()); err != nil {
			t.Errorf("material: %v", err)
		}
	})

	t.Run("a grant for another subject", func(t *testing.T) {
		c := verify(t, s.mint(t, func(_, gg map[string]any) { gg["subject"] = "customer:C-9999" }), keys)
		if err := c.CheckSubject(theSubject); err == nil {
			t.Fatal("Alice's approval authorised Bob's call")
		}
	})

	t.Run("a changed material value", func(t *testing.T) {
		// The approver saw 1250. The caller sends 125000. Same tool, same
		// subject, same task, same signature.
		sent := approvedMaterial()
		sent["amount_minor_units"] = "125000"
		c := verify(t, s.mint(t, nil), keys)
		err := c.CheckMaterial(sent)
		if err == nil {
			t.Fatal("a request that differs from what was approved was accepted")
		}
		// Deliberately says nothing about WHICH field: the caller knows its
		// own values, and naming the difference would only help a prober.
		if strings.Contains(err.Error(), "amount_minor_units") {
			t.Errorf("the refusal names the differing field: %v", err)
		}
	})

	t.Run("an expired grant", func(t *testing.T) {
		past := time.Now().Add(-2 * time.Hour)
		c := verify(t, s.mint(t, func(cl, _ map[string]any) {
			cl["iat"] = float64(past.Unix())
			cl["exp"] = float64(past.Add(15 * time.Minute).Unix())
		}), keys)
		if err := c.CheckFresh(time.Now(), 30*time.Second, 0); err == nil {
			t.Fatal("an expired grant was accepted")
		}
		// And separately: within its own expiry but older than the maximum
		// the tool declares. The tool's ceiling is not the issuer's to raise.
		recent := time.Now().Add(-30 * time.Minute)
		c2 := verify(t, s.mint(t, func(cl, _ map[string]any) {
			cl["iat"] = float64(recent.Unix())
			cl["exp"] = float64(time.Now().Add(time.Hour).Unix())
		}), keys)
		if err := c2.CheckFresh(time.Now(), 30*time.Second, 0); err != nil {
			t.Fatalf("a grant inside its own expiry was refused: %v", err)
		}
		if err := c2.CheckFresh(time.Now(), 30*time.Second, 15*time.Minute); err == nil {
			t.Fatal("a grant older than the declared maximum was accepted; an issuer " +
				"minting a long approval for a tool that asked for fifteen minutes " +
				"must not get it")
		}
	})
}

// A grant minted from a delegated identity is refused on shape, not policy —
// the check that stops an agent approving its own irreversible call.
func TestADelegatedApproverIsRefused(t *testing.T) {
	s := newSigner(t)
	c := verify(t, s.mint(t, func(cl, _ map[string]any) {
		cl["act"] = map[string]any{"sub": "agent:bank.agents.v1.Assistant"}
	}), s.keys(t))
	if err := c.CheckNoAct(); err == nil {
		t.Fatal("a grant carrying a delegation chain was accepted")
	}
}

// A delegation token is not a grant, and must not be spendable as one.
func TestATokenWithNoGrantClaimIsNotAGrant(t *testing.T) {
	s := newSigner(t)
	raw := s.mint(t, nil)
	// Re-mint without the garm_grant claim at all.
	payload, err := json.Marshal(map[string]any{"iss": "https://sts.example.test", "sub": "amir"})
	if err != nil {
		t.Fatal(err)
	}
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: s.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", s.kid))
	if err != nil {
		t.Fatal(err)
	}
	obj, err := sig.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Verify(plain, s.keys(t)); err == nil {
		t.Fatal("a token with no garm_grant claim verified as an approval")
	}
	if _, err := grants.Verify(raw, s.keys(t)); err != nil {
		t.Fatalf("the control grant stopped verifying: %v", err)
	}
}

// Signed by a key the published set does not hold: judged, and judged bad.
// Separable from "the key set could not be read", which is nobody's fault but
// the operator's — hence the sentinel.
func TestAGrantSignedByAnUnpublishedKey(t *testing.T) {
	mint := newSigner(t)
	published := newSigner(t)
	_, err := grants.Verify(mint.mint(t, nil), published.keys(t))
	if err == nil {
		t.Fatal("a grant signed by an unpublished key verified")
	}
	if !errors.Is(err, grants.ErrNoSuchKey) {
		t.Errorf("the failure does not wrap ErrNoSuchKey, so a caller cannot tell "+
			"'this grant is bad' from 'the key set could not be read': %v", err)
	}
}

// A tampered body does not verify, which is the one thing the signature is
// for.
func TestATamperedGrantDoesNotVerify(t *testing.T) {
	s := newSigner(t)
	raw := s.mint(t, nil)
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("a compact JWS has three parts, got %d", len(parts))
	}
	// Swap one character of the payload segment for another valid base64url
	// character, so the shape survives and only the signature fails.
	body := []byte(parts[1])
	if body[0] == 'e' {
		body[0] = 'f'
	} else {
		body[0] = 'e'
	}
	if _, err := grants.Verify(parts[0]+"."+string(body)+"."+parts[2], s.keys(t)); err == nil {
		t.Fatal("a tampered grant verified")
	}
}

// The approver predicate: the tool says how senior, the grant says how senior
// this one was.
func TestTheApproverPredicate(t *testing.T) {
	s := newSigner(t)
	c := verify(t, s.mint(t, nil), s.keys(t))
	if err := c.CheckApprover(toolv1.Clearance_CLEARANCE_RESTRICTED, []string{"financial"}); err != nil {
		t.Fatalf("an approver who meets the predicate was refused: %v", err)
	}
	if err := c.CheckApprover(toolv1.Clearance_CLEARANCE_RESTRICTED, []string{"compliance"}); err == nil {
		t.Fatal("an approver without the compartment was accepted; compartments are a " +
			"set and holding one does not admit you to another")
	}

	// The prefixed and bare spellings both read: the STS mints RESTRICTED and
	// the devkit mints CLEARANCE_RESTRICTED, and an approval refused over a
	// prefix would be maddening.
	prefixed := verify(t, s.mint(t, func(_, gg map[string]any) {
		gg["approver_clearance"] = "CLEARANCE_RESTRICTED"
	}), s.keys(t))
	if err := prefixed.CheckApprover(toolv1.Clearance_CLEARANCE_RESTRICTED, nil); err != nil {
		t.Errorf("the prefixed spelling was refused: %v", err)
	}
}

// A tool that binds no material fields binds none; a tool that does, and a
// grant with no digest, is an approval of the tool rather than of the call.
func TestMaterialBinding(t *testing.T) {
	s := newSigner(t)
	c := verify(t, s.mint(t, nil), s.keys(t))
	if err := c.CheckMaterial(nil); err != nil {
		t.Errorf("a call binding nothing was refused: %v", err)
	}
	none := verify(t, s.mint(t, func(_, gg map[string]any) { delete(gg, "material") }), s.keys(t))
	if err := none.CheckMaterial(approvedMaterial()); err == nil {
		t.Fatal("a grant with no digest approved a call that binds material fields")
	}
}

func verify(t *testing.T, raw string, keys grants.KeySource) *grants.Claims {
	t.Helper()
	c, err := grants.Verify(raw, keys)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	return c
}
