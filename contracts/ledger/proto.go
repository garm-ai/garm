package ledger

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	ledgerv1 "github.com/garm-ai/garm/contracts/garm/ledger/v1"
)

// ToProto converts an Event for the wire.
//
// It lives here, beside the struct, so that the two shapes are edited
// together. A field added to Event and not to this function is a field that
// reaches the recorder and never reaches the lake — visible nowhere, because
// nothing fails.
func ToProto(ev Event) *ledgerv1.Event {
	out := &ledgerv1.Event{
		EventId:       ev.ID,
		Tenant:        ev.Tenant,
		App:           ev.App,
		Feature:       ev.Feature,
		RunId:         ev.RunID,
		CorrelationId: ev.CorrelationID,
		CausationId:   ev.CausationID,
		BudgetId:      ev.BudgetID,
		Tags:          ev.Tags,

		PromptName:      ev.PromptName,
		PromptHash:      ev.PromptHash,
		Alias:           ev.Alias,
		ResolvedModel:   ev.ResolvedModel,
		ModelOverridden: ev.ModelOverridden,

		Usage: &ledgerv1.Usage{
			InputTokens:     ev.Usage.InputTokens,
			OutputTokens:    ev.Usage.OutputTokens,
			CachedTokens:    ev.Usage.CachedTokens,
			ReasoningTokens: ev.Usage.ReasoningTokens,
		},
		CostUsd:    ev.CostUSD,
		CostSource: ev.CostSource,
		LatencyMs:  ev.LatencyMS,

		ProviderRequestId: ev.ProviderRequestID,
		FallbackUsed:      ev.FallbackUsed,
		Outcome:           string(ev.Outcome),
		ErrorKind:         ev.ErrorKind,

		PolicyMode:       ev.PolicyMode,
		PolicyViolations: ev.PolicyViolations,

		Tool:                  ev.Tool,
		PrincipalSubject:      ev.PrincipalSubject,
		PrincipalActor:        ev.PrincipalActor,
		PrincipalKind:         ev.PrincipalKind,
		ChainDepth:            int32(ev.ChainDepth),
		ClearanceEffective:    ev.ClearanceEffective,
		CompartmentsEffective: ev.CompartmentsEffective,
		RedactionPlan:         ev.RedactionPlan,
		RedactionCount:        int32(ev.RedactionCount),
		DisclosedCount:        int32(ev.DisclosedCount),

		ErrorDetail: ev.ErrorDetail,
	}
	if !ev.Time.IsZero() {
		out.Time = timestamppb.New(ev.Time)
	}
	return out
}

// FromProto is the reader's half, for a forwarder turning a stream back into
// rows. Kept beside ToProto so a field can only be forgotten in both at once.
func FromProto(p *ledgerv1.Event) Event {
	if p == nil {
		return Event{}
	}
	ev := Event{
		ID:            p.GetEventId(),
		Tenant:        p.GetTenant(),
		App:           p.GetApp(),
		Feature:       p.GetFeature(),
		RunID:         p.GetRunId(),
		CorrelationID: p.GetCorrelationId(),
		CausationID:   p.GetCausationId(),
		BudgetID:      p.GetBudgetId(),
		Tags:          p.GetTags(),

		PromptName:      p.GetPromptName(),
		PromptHash:      p.GetPromptHash(),
		Alias:           p.GetAlias(),
		ResolvedModel:   p.GetResolvedModel(),
		ModelOverridden: p.GetModelOverridden(),

		Usage: Usage{
			InputTokens:     p.GetUsage().GetInputTokens(),
			OutputTokens:    p.GetUsage().GetOutputTokens(),
			CachedTokens:    p.GetUsage().GetCachedTokens(),
			ReasoningTokens: p.GetUsage().GetReasoningTokens(),
		},
		CostUSD:    p.GetCostUsd(),
		CostSource: p.GetCostSource(),
		LatencyMS:  p.GetLatencyMs(),

		ProviderRequestID: p.GetProviderRequestId(),
		FallbackUsed:      p.GetFallbackUsed(),
		Outcome:           Outcome(p.GetOutcome()),
		ErrorKind:         p.GetErrorKind(),

		PolicyMode:       p.GetPolicyMode(),
		PolicyViolations: p.GetPolicyViolations(),

		Tool:                  p.GetTool(),
		PrincipalSubject:      p.GetPrincipalSubject(),
		PrincipalActor:        p.GetPrincipalActor(),
		PrincipalKind:         p.GetPrincipalKind(),
		ChainDepth:            int(p.GetChainDepth()),
		ClearanceEffective:    p.GetClearanceEffective(),
		CompartmentsEffective: p.GetCompartmentsEffective(),
		RedactionPlan:         p.GetRedactionPlan(),
		RedactionCount:        int(p.GetRedactionCount()),
		DisclosedCount:        int(p.GetDisclosedCount()),

		ErrorDetail: p.GetErrorDetail(),
	}
	if t := p.GetTime(); t != nil {
		ev.Time = t.AsTime()
	}
	return ev
}
