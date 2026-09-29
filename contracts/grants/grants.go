// Package grants is the part of grant verification that is contract logic
// rather than daemon policy.
//
// A grant is a short-lived, single-use token saying that a named person, at a
// named authority, approved a named call over named values. Three processes
// have to agree about what one says: the STS that mints it, the daemon that
// spends it at the gate, and now the tasks service, which must refuse a
// decision whose grant names another task or values the approver never saw.
//
// Until now that agreement lived in `garmd/internal/grants`, which nothing
// outside garmd can import — so a second service either reimplemented the
// checks or skipped them. Neither is acceptable for the one credential in the
// system that authorises an irreversible act. So the shared half moves here,
// beside `contracts/grant` (which already owns the digest both sides compute)
// for exactly the reason that package gives: two independent derivations of
// one security-relevant value is a divergence waiting to happen, and a shared
// function is cheaper than a detector.
//
// # What is here, and what is deliberately not
//
// Here: parsing a grant, verifying its signature against a key set the caller
// supplies, reading its claims, and comparing a material map — or a request
// message — against the digest it carries. Every one of those is a fact about
// the token's format, identical in every process that reads one.
//
// Not here, and staying in the daemon:
//
//   - The replay cache. Single-use is enforced by a store, and which store is
//     a deployment's decision. A tool that spends a grant has its own row to
//     write it on.
//   - Issuer and audience configuration. Which issuers a process trusts and
//     what it calls itself are deployment policy; this package reads `iss`
//     and `aud` off the token and hands them back for the caller to judge.
//   - Refusal shaping. garmd's ErrRefused / ErrGrantRequired / ErrUnavailable
//     distinction exists so a SURFACE can answer a code and an operator can
//     be paged for the right thing. Those are three different next moves for
//     a caller, and they are the daemon's to decide. Here, an error is an
//     error and says which check failed.
//
// # The rule these checks exist to hold
//
// "No `act` on an approver" is enforced in three places on purpose (the STS
// at mint, the tasks tool at decide, the daemon at spend) so that the design
// survives one of them being wrong. This package is what lets the second and
// third be the same code without being the same process.
package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	toolv1 "github.com/garm-ai/garm/contracts/garm/tool/v1"
	"github.com/garm-ai/garm/contracts/grant"
)

// PermittedAlgorithms is the signature algorithm allowlist.
//
// Exported, and the same list garmd's token verifier uses, because two
// allowlists is two things to widen and the second one is the one nobody
// remembers to look at. `none` is absent, which is the point of having a list
// at all: go-jose will not verify an algorithm that is not named here, so a
// token claiming `alg: none` is refused before its claims are read.
var PermittedAlgorithms = []jose.SignatureAlgorithm{
	jose.ES256, jose.ES384, jose.ES512,
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
}

// ErrNoSuchKey means the grant names a key the supplied key set does not hold.
//
// Distinguished from every other failure of a KeySource because the two mean
// opposite things to an operator: a kid nobody publishes is a grant this
// process can JUDGE — bad — while a key set that could not be read at all is
// this deployment's own failure and nothing has been judged. The caller
// decides which of its own sentinels to wrap each in.
var ErrNoSuchKey = errors.New("no key with that id")

// KeySource hands back the public key a grant was signed with.
//
// An interface rather than a concrete JWKS client, because the two consumers
// fetch keys very differently: the daemon holds a cached, refreshing key set
// per trusted issuer, and a tool service may hold one static document. What
// they share is this question.
//
// Implementations that cannot answer must return an error that does NOT wrap
// ErrNoSuchKey, so "the issuer does not publish this key" stays separable
// from "the issuer could not be reached".
type KeySource interface {
	Key(kid string) (any, error)
}

// StaticKeys is a KeySource over a JWKS document, for a process that holds
// one key set and does not refresh it.
//
// Enough for a tool service verifying grants from one STS, and deliberately
// not more: a refreshing, multi-issuer key set is a daemon's concern and
// carries a cache, a clock and a failure mode this package does not want.
type StaticKeys struct{ set jose.JSONWebKeySet }

// ParseJWKS reads a JWKS document.
func ParseJWKS(doc []byte) (*StaticKeys, error) {
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(doc, &set); err != nil {
		return nil, fmt.Errorf("the key set is not a JWKS document: %w", err)
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("the key set is empty, so no grant could ever be verified")
	}
	return &StaticKeys{set: set}, nil
}

// Key implements KeySource.
func (s *StaticKeys) Key(kid string) (any, error) {
	for _, k := range s.set.Key(kid) {
		return k.Key, nil
	}
	return nil, fmt.Errorf("%w: %q", ErrNoSuchKey, kid)
}

// Claims is one approval, as the token carries it.
//
// Every field is read, none is judged. A check here would be a policy
// decision taken in the wrong place: whether an issuer is trusted, whether
// this deployment is the audience, and what to do about a claim that is
// missing are all the caller's, and they differ between a daemon at the gate
// and a tool service deciding a task.
type Claims struct {
	Issuer   string
	Audience []string

	// ID is `jti`, and it is what makes a grant single-use. The store that
	// records a spend is the caller's; this only reports the identity it
	// would be recorded under.
	ID string

	IssuedAt time.Time
	Expires  time.Time

	// Tool is the FQN this approval is for. Without it, an approval for
	// get_balance is spendable on initiate_payment.
	Tool string

	// Subject is whose call was approved. Without it, Alice's approval
	// authorises Bob's payment.
	Subject string

	// Task is the task this approval was given on, and it is the claim this
	// package exists for. The STS copies it in without looking the task up,
	// which is what binds one grant to one task: two tasks over identical
	// material — the same payment asked twice — cannot share a grant.
	//
	// The daemon deliberately ignores it: it compares tool, subject, expiry,
	// age, approver and digest, and a claim it does not read cannot fail
	// those. The tasks service is what reads it.
	Task string

	// Material is the digest over the values the human saw.
	Material string

	// Approver is who clicked, and what authority they held. The tool says
	// how senior an approver must be; the grant says how senior this one was.
	Approver             string
	ApproverClearance    string
	ApproverCompartments []string

	// HasAct records whether the token carried a delegation chain. A
	// delegated identity may not approve: it is what structurally stops an
	// agent approving its own destructive act. Not a policy anybody
	// configures — a shape the token may not have.
	HasAct bool
}

// Verify parses a grant, checks its signature against keys, and returns its
// claims.
//
// It checks nothing about what the claims SAY. That is the caller's, through
// the Check* methods below, because the daemon and the tasks service check
// overlapping but different sets and neither should inherit the other's.
func Verify(raw string, keys KeySource) (*Claims, error) {
	if keys == nil {
		return nil, errors.New("no key source: a grant verified against nothing is a grant nobody checked")
	}
	sig, err := jose.ParseSigned(raw, PermittedAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("the grant is not a well-formed token: %w", err)
	}
	if len(sig.Signatures) != 1 {
		return nil, fmt.Errorf("the grant carries %d signatures, want 1", len(sig.Signatures))
	}
	kid := sig.Signatures[0].Header.KeyID
	if kid == "" {
		return nil, errors.New("the grant has no kid, so the key that signed it cannot be named")
	}
	key, err := keys.Key(kid)
	if err != nil {
		return nil, fmt.Errorf("the grant's key: %w", err)
	}
	payload, err := sig.Verify(key)
	if err != nil {
		return nil, fmt.Errorf("the grant's signature: %w", err)
	}
	return ParseClaims(payload)
}

// ParseClaims reads a grant's claims out of a VERIFIED token body.
//
// Separate from Verify so a caller that already verified a signature — a test
// double, a daemon whose key handling is its own — can reuse the claim
// reading without a second JOSE dependency. It is not an entry point for an
// unverified token, and a caller that treats it as one has verified nothing.
func ParseClaims(payload []byte) (*Claims, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("the grant's body is not JSON: %w", err)
	}
	g, _ := raw["garm_grant"].(map[string]any)
	if g == nil {
		return nil, errors.New("the token carries no garm_grant claim, so it is not " +
			"an approval — a delegation token is not a grant and must not be " +
			"spendable as one")
	}
	_, hasAct := raw["act"]

	return &Claims{
		Issuer:               str(raw, "iss"),
		Audience:             strSlice(raw, "aud"),
		ID:                   str(raw, "jti"),
		IssuedAt:             unix(raw, "iat"),
		Expires:              unix(raw, "exp"),
		Tool:                 str(g, "tool"),
		Subject:              str(g, "subject"),
		Task:                 str(g, "task"),
		Material:             str(g, "material"),
		Approver:             str(g, "approver"),
		ApproverClearance:    str(g, "approver_clearance"),
		ApproverCompartments: strSlice(g, "approver_compartments"),
		HasAct:               hasAct,
	}, nil
}

// CheckNoAct refuses a grant minted from a delegated identity.
func (c *Claims) CheckNoAct() error {
	if c.HasAct {
		return errors.New("the grant carries a delegation chain; an approval must be " +
			"given by a person acting as themselves, or an agent could approve its " +
			"own irreversible call")
	}
	return nil
}

// CheckTool refuses a grant minted for another tool.
func (c *Claims) CheckTool(fqn string) error {
	if c.Tool != fqn {
		return fmt.Errorf("the grant approves %q and this call is %q", c.Tool, fqn)
	}
	return nil
}

// CheckSubject refuses a grant minted for another caller's call — the
// confused deputy this check exists for.
func (c *Claims) CheckSubject(subject string) error {
	if subject == "" || c.Subject != subject {
		return fmt.Errorf("the grant approves a call by %q, not by this caller", c.Subject)
	}
	return nil
}

// CheckTask refuses a grant given on another task.
//
// The claim the daemon ignores and the tasks service must not. Two tasks over
// identical material digest identically, so without this a person approving
// one payment authorises the other.
func (c *Claims) CheckTask(taskID string) error {
	if taskID == "" {
		return errors.New("no task id to check the grant against")
	}
	if c.Task == "" {
		return errors.New("the grant carries no task claim, so it approves the material " +
			"rather than this decision; two tasks over the same values would share it")
	}
	if c.Task != taskID {
		return fmt.Errorf("the grant was given on task %q and this decision is on %q", c.Task, taskID)
	}
	return nil
}

// CheckFresh refuses a grant that has expired, or that is older than the
// tool's declared maximum.
//
// maxAge zero means the tool declared none. The tool's declaration is a
// CEILING the issuer cannot raise: an issuer minting a day-long approval for
// a tool that asked for fifteen minutes gets fifteen minutes, because the
// tool is the thing that knows how stale an approval may be for what it does.
func (c *Claims) CheckFresh(now time.Time, skew, maxAge time.Duration) error {
	if !c.Expires.IsZero() && now.After(c.Expires.Add(skew)) {
		return fmt.Errorf("the grant expired at %s", c.Expires.UTC().Format(time.RFC3339))
	}
	if maxAge <= 0 {
		return nil
	}
	if c.IssuedAt.IsZero() {
		return errors.New("the grant has no iat, so its age cannot be checked against the maximum")
	}
	if age := now.Sub(c.IssuedAt); age > maxAge+skew {
		return fmt.Errorf("the grant is %s old and this accepts approvals up to %s",
			age.Truncate(time.Second), maxAge)
	}
	return nil
}

// CheckApprover holds the grant to the authority the approval required.
//
// The predicate comes from the tool's approval block, or from the task's
// stored predicate when a task service is asking — the same comparison either
// way, which is why it takes values rather than reading a policy.
func (c *Claims) CheckApprover(min toolv1.Clearance, compartments []string) error {
	if min != toolv1.Clearance_CLEARANCE_UNSPECIFIED {
		got, ok := toolv1.Clearance_value[normaliseClearance(c.ApproverClearance)]
		if !ok || toolv1.Clearance(got) < min {
			return fmt.Errorf("the approver held %q and this requires at least %s",
				c.ApproverClearance, min)
		}
	}
	for _, need := range compartments {
		if !contains(c.ApproverCompartments, need) {
			return fmt.Errorf("the approver does not hold the %q compartment this requires "+
				"of an approver", need)
		}
	}
	return nil
}

// CheckMaterial compares the values a caller holds with the digest the grant
// carries.
//
// `values` is a map of dotted path to canonical text — what the tasks service
// stored when the task was opened, or what Materialise read off the request
// about to be sent. Both sides digest it with contracts/grant.Digest, which
// is the one implementation of that encoding.
//
// The mismatch error deliberately does not say which field differs. The
// caller sent the values and knows them; naming the difference would only
// help somebody probing what an approval covered.
func (c *Claims) CheckMaterial(values map[string]string) error {
	if len(values) == 0 {
		// Nothing declared, so nothing bound. The grant covers the tool for a
		// window — weak for anything irreversible, which is why the linter
		// pushes destructive tools toward declaring material fields, but it
		// is the author's declaration and not this code's to override.
		return nil
	}
	if c.Material == "" {
		return errors.New("this call binds a grant to its material fields and the grant " +
			"carries no digest, so it approves the tool rather than the call")
	}
	if c.Material != grant.Digest(values) {
		return errors.New("the request does not match what was approved")
	}
	return nil
}

// Small readers over a decoded token body. Deliberately tolerant of shape and
// intolerant of meaning: an absent claim reads as its zero and a Check decides
// whether that is fatal, so "the claim was missing" and "the claim said
// something wrong" stay two different errors rather than one parse failure.

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// strSlice tolerates a single string where a list is expected, because
// issuers differ on a single-element `aud`, and drops non-strings rather than
// failing the whole token for one bad entry.
func strSlice(m map[string]any, k string) []string {
	switch v := m[k].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func unix(m map[string]any, k string) time.Time {
	switch v := m[k].(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	}
	return time.Time{}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// normaliseClearance accepts the bare and prefixed spellings — the STS mints
// CONFIDENTIAL, the devkit mints CLEARANCE_CONFIDENTIAL, and an approval
// refused over a prefix would be maddening.
func normaliseClearance(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "CLEARANCE_") {
		s = "CLEARANCE_" + s
	}
	if _, ok := toolv1.Clearance_value[s]; !ok {
		return ""
	}
	return s
}
