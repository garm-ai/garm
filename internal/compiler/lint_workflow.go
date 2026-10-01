package compiler

import (
	"fmt"
	"sort"
	"strings"

	"cel.dev/cel-go/cel"
	celast "cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/common/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	agentv1 "github.com/garm-ai/contracts/garm/agent/v1"
	toolv1 "github.com/garm-ai/contracts/garm/tool/v1"
	"github.com/garm-ai/contracts/policy"
)

// lintAgentMode covers A8: the mode and the fields agree.
//
// A mode enum earns its keep by making the invalid combinations named rather
// than reachable states nobody tested. Every refusal here names the field, so
// an author is told what to remove and not merely that something is wrong.
//
// A1 decides what methods a service may HAVE; this rule decides which are
// valid IN THIS MODE. So a GetState on a react agent is refused here, not by
// A1 — one rule per question, so an author gets one diagnostic per mistake
// rather than two for the same one.
func lintAgentMode(a Agent) []Diag {
	var out []Diag
	p := a.Policy
	bad := func(format string, args ...any) {
		out = append(out, Diag{Rule: "A8", Path: string(a.FQN),
			Msg: fmt.Sprintf(format, args...)})
	}

	switch p.GetMode() {
	case agentv1.Mode_MODE_WORKFLOW:
		if len(p.GetSteps()) == 0 {
			bad("mode is MODE_WORKFLOW and declares no steps; a workflow is a graph")
		}
		if len(p.GetInitial()) == 0 {
			bad("mode is MODE_WORKFLOW and declares no initial; `initial` is the " +
				"only place a caller's request enters the state")
		}
		// A graph of tool calls makes no generation call of its own, so both of
		// these would be dead weight in the annotation — and dead weight is what
		// teaches a reviewer to skim.
		if len(p.GetPrompts()) > 0 {
			bad("mode is MODE_WORKFLOW and declares prompts; a graph of tool " +
				"calls makes no generation call, so remove them (the model " +
				"belongs to whichever agent a step invokes)")
		}
		if p.GetModel() != nil {
			bad("mode is MODE_WORKFLOW and declares a model; a graph of tool " +
				"calls makes no generation call, so remove it")
		}
		if a.StateMessage == nil {
			bad("mode is MODE_WORKFLOW and the service declares no GetState; " +
				"GetState's output type IS the state type, so there is nothing " +
				"for the graph's expressions to be checked against")
		}
	case agentv1.Mode_MODE_REACT:
		if p.GetPrompts()["system"] == nil {
			bad("mode is MODE_REACT and has no prompts[\"system\"]")
		}
		for field, n := range map[string]int{
			"steps": len(p.GetSteps()), "edges": len(p.GetEdges()),
			"initial": len(p.GetInitial()),
		} {
			if n > 0 {
				bad("mode is MODE_REACT and declares %s; react mode has no graph", field)
			}
		}
		if a.StateMessage != nil {
			bad("mode is MODE_REACT and the service declares GetState; react " +
				"mode has no state to read")
		}
	default:
		bad("mode is unset; declare MODE_REACT or MODE_WORKFLOW")
	}
	return out
}

// lintWorkflowGraph covers A6: the graph is a graph.
func lintWorkflowGraph(a Agent) []Diag {
	p := a.Policy
	if p.GetMode() != agentv1.Mode_MODE_WORKFLOW {
		return nil
	}
	var out []Diag
	g, probs := newGraph(p.GetSteps(), p.GetEdges())
	for _, s := range probs {
		out = append(out, Diag{Rule: "A6", Path: string(a.FQN), Msg: s})
	}
	if g == nil {
		return out
	}
	// Control flow must be TOTAL. A step with outgoing edges needs at least one
	// unconditional one, so "no predicate matched" cannot arise: without this a
	// typo in a `when` ends a run early and silently, which is the failure mode
	// that looks like success.
	for _, id := range g.ids {
		es := g.succ[id]
		if len(es) == 0 {
			continue // terminal, by having no outgoing edge
		}
		total := false
		for _, e := range es {
			if e.When == "" {
				total = true
				break
			}
		}
		if !total {
			out = append(out, Diag{Rule: "A6", Path: string(a.FQN), Msg: fmt.Sprintf(
				"every outgoing edge of step %q carries a `when`, so a run reaches "+
					"it and stops if none matches. Add one unconditional edge as the "+
					"fallback, or make %q terminal by removing its edges", id, id)})
		}
	}
	return out
}

// lintWorkflowExpressions covers A7: every expression type-checks, and a state
// field may be read only where every path to that point has written it.
//
// The rule with teeth is the second half. A declared state message returns the
// ZERO value for a field nothing wrote — silently — and a payment reference
// reading "INV-1 / compliance " with nothing after it is a wrong answer that
// looks like a right one. That is worse than a missing value, so the analysis
// is on WRITES: a field's writers are `initial` plus the `set` blocks naming
// it, and for a step writer the question is whether it DOMINATES the reading
// step. graph.dominators answers exactly that.
//
// This entry point is the whole-catalogue one. See lintWorkflowExpressionsWith
// for what a one-directory run can and cannot answer.
func lintWorkflowExpressions(a Agent, tools map[string]Tool) []Diag {
	// nil: no sibling agent's GetRun output to substitute for an agent step's
	// `response`. Every existing caller of this entry point lints a single
	// agent whose steps call plain tools, never another agent's Invoke, so
	// the substitution map below would be consulted and found empty either
	// way. lintAgents (the production path) builds the real map once, over
	// every agent in the linted set, and calls lintWorkflowExpressionsWith
	// directly — see agentStepReplies.
	return lintWorkflowExpressionsWith(a, tools, nil, Options{})
}

// lintWorkflowExpressionsWith is A7 with the caller's context.
//
// A7 IS A WHOLE-CATALOGUE RULE: a step's `with` keys resolve against the
// tool's REQUEST descriptor and `set` reads its RESPONSE, both usually in
// another directory. buf invokes the protoc plugin once per directory, so
// under Options.PartialSet those descriptors are not in the request at all.
// The repository's pattern — lint.go's preamble, and A3, A9 and P1 — is that
// such a rule WARNS there and is enforced where the whole set is assembled.
// So under PartialSet a step whose tool is absent gets a warning naming the
// commands that do check it, and the halves that need only the agent's own
// declaration still ERROR:
//
//   - the allowlist (the graph may not name a tool the manifest does not),
//   - `set` keys against the state message, which is in the agent's own file,
//   - `initial`, over Invoke's request, also the agent's own file,
//   - edge predicates, which read `state` and nothing else,
//   - and the dominance rule itself, on every `with` and `set` expression:
//     the reads are found by PARSING, which needs no tool descriptor.
//
// That last line is the point of the split. The load-bearing half of A7 runs
// everywhere, including on every `buf generate`; only the type-fitting of a
// `with` key against a request it cannot see waits for `garm lint`.
//
// agentReplies is what a step whose tool is an AGENT's Invoke actually sees
// at `response`. See the note above the `response` substitution below, and
// agentStepReplies for how the map is built. nil (every PartialSet caller,
// and every existing caller of lintWorkflowExpressions) means no substitution
// is available and `response` stays each tool's own declared output — the
// behaviour this function always had.
func lintWorkflowExpressionsWith(a Agent, tools map[string]Tool,
	agentReplies map[string]protoreflect.MessageDescriptor, opts Options) []Diag {
	p := a.Policy
	if p.GetMode() != agentv1.Mode_MODE_WORKFLOW {
		return nil
	}
	var out []Diag
	bad := func(format string, args ...any) {
		out = append(out, Diag{Rule: "A7", Path: string(a.FQN),
			Msg: fmt.Sprintf(format, args...)})
	}

	allowed := map[string]bool{}
	for _, ref := range p.GetTools() {
		allowed[ref.GetFqn()] = true
	}
	g, _ := newGraph(p.GetSteps(), p.GetEdges())
	if g == nil {
		// A6 reported the shape. Going on would be a second diagnostic for the
		// same mistake, and a dominator set over a graph with two entries means
		// nothing anyway.
		return out
	}

	state := a.StateMessage
	if state == nil {
		// A8 says this too. Repeated here because A7 cannot proceed without
		// it and a rule that returns silently is one nobody can tell ran.
		bad("mode is MODE_WORKFLOW and no GetState method declares a state " +
			"type, so there is nothing for these expressions to be checked against")
		return out
	}

	// writesBy[step] is the state paths that step's `set` writes.
	dom := g.dominators()
	writesBy := map[string]map[string]bool{}
	for _, s := range p.GetSteps() {
		w := map[string]bool{}
		for k := range s.GetSet() {
			w[k] = true
		}
		writesBy[s.GetId()] = w
	}
	initial := map[string]bool{}
	for k := range p.GetInitial() {
		initial[k] = true
	}
	// writtenBefore is the fields guaranteed written when a step BEGINS: the
	// `initial` block, plus the writes of every step that dominates it. A
	// step's own `set` is not in it — a `with` is evaluated before the call
	// and a `set` from the pre-call state, so neither can read this step's
	// own writes.
	writtenBefore := func(step string) map[string]bool {
		w := copySet(initial)
		for d := range dom[step] {
			if d == step {
				continue
			}
			for f := range writesBy[d] {
				w[f] = true
			}
		}
		return w
	}
	// whoWrites is for the diagnostic alone: naming the steps that write a
	// field is what lets an author see which path skips them.
	whoWrites := func(field string) []string {
		var who []string
		for _, id := range g.ids {
			for f := range writesBy[id] {
				if pathCovers(f, field) {
					who = append(who, id)
					break
				}
			}
		}
		return who
	}

	// One environment per shape, not per step: building one walks a file's
	// whole type graph, and every step's `with` sees the same `state`.
	stateOnly, err := stateEnv(state, nil)
	if err != nil {
		bad("cannot build an expression environment over %s: %v", state.FullName(), err)
		return out
	}
	setEnvs := map[protoreflect.FullName]*cel.Env{}

	out = append(out, lintWorkflowInitial(a, state, initial)...)
	out = append(out, absentToolWarning(a, allowed, tools, opts)...)

	for _, s := range p.GetSteps() {
		id := s.GetId()
		avail := writtenBefore(id)

		// The reads come first, and they run whatever the caller can see: a
		// parse is enough to find them, so PartialSet changes nothing here.
		own := writesBy[id]
		checkReads(bad, state, avail, own, whoWrites, id, "with", s.GetWith())
		checkReads(bad, state, avail, own, whoWrites, id, "set", s.GetSet())

		if !allowed[s.GetTool()] {
			bad("step %q calls %s, which is not in this agent's tools; the "+
				"allowlist is the single authority on what may be called and a "+
				"graph may only order what it already permits", id, s.GetTool())
			continue
		}
		tool, haveTool := tools[s.GetTool()]
		if !haveTool {
			if !opts.PartialSet {
				bad("step %q calls %s, which no linted package declares", id, s.GetTool())
				continue
			}
			// absentToolWarning has already said, once, that this run cannot
			// see the tool. What the agent's OWN file still answers is checked
			// here: the `set` keys resolve against the state message, and every
			// `with` expression is compiled against `state` — only the type FIT
			// against a request field needs the tool that is missing.
			checkSetKeys(bad, state, id, s.GetSet())
			checkWithCompiles(bad, stateOnly, id, s.GetWith())
			continue
		}

		req, resp := tool.Method.Input(), tool.Method.Output()
		// A step whose tool is an AGENT's Invoke answers garm.agent.v1.RunRef
		// at the call itself, but that is a reference, not the answer: the
		// runner awaits the run it names and the step's `set` actually sees
		// that child's GetRun output, garm.agent.v1.RunStatus — design §6.1
		// frames 10e-10f, and the runner's own internal/run.responseType.
		// Checking `set` against RunRef's two fields (just run_id) would
		// refuse `response.state`, `response.error` and every other field
		// RunStatus adds, which is exactly the lint/runtime disagreement this
		// substitution exists to close. See agentStepReplies for how the map
		// is built. An agent with no GetRun at all is a legitimate
		// declaration (design §2.2: at most one) that agentStepReplies
		// leaves out of the map on purpose, so `response` falls back to
		// RunRef for it — unchanged from today, and no worse than today: the
		// runner refuses such a step at run time regardless of what `set`
		// reads (internal/run.responseType), so this is not a new gap.
		//
		// A PLAIN tool that happens to answer RunRef (nothing requires that
		// it not) is deliberately NOT substituted: agentReplies only has an
		// entry when s.GetTool() IS some agent's Invoke, so such a tool's
		// `response` stays RunRef, its own declared output — the same
		// distinction agentd's verifyGraph draws between an agentStep and an
		// unclaimedRunRef.
		if resp.FullName() == runRefName {
			if status, ok := agentReplies[s.GetTool()]; ok {
				resp = status
			}
		}
		for _, key := range sortedKeys(s.GetWith()) {
			expr := s.GetWith()[key]
			fd, err := resolveFieldPath(req, key)
			if err != nil {
				bad("step %q: `with` key %q: %v", id, key, err)
				continue
			}
			if isRunnerOwned(tool, key) {
				bad("step %q sets %q, which is the runner's: agentd fills it with "+
					"`<run id>-<dispatch seq>`, after `with` has been evaluated, so "+
					"an author's value is both overwritten and a way to turn a retry "+
					"into a second call. Remove it", id, key)
				continue
			}
			ast, iss := stateOnly.Compile(expr)
			if iss != nil && iss.Err() != nil {
				bad("step %q: `with[%s]` %q does not compile: %s",
					id, key, expr, celMessages(iss))
				continue
			}
			if !celTypeFits(ast.OutputType(), fd) {
				bad("step %q: `with[%s]` is %s but %s is %s; a mismatch here is a "+
					"marshalling failure at run time, in a governed call", id, key,
					ast.OutputType(), key, fieldType(fd))
			}
		}

		// A `set` may additionally read `response`.
		senv, ok := setEnvs[resp.FullName()]
		if !ok {
			senv, err = stateEnv(state, map[string]protoreflect.MessageDescriptor{
				"response": resp})
			if err != nil {
				bad("step %q: cannot build a `set` environment over %s: %v",
					id, resp.FullName(), err)
				continue
			}
			setEnvs[resp.FullName()] = senv
		}
		for _, key := range sortedKeys(s.GetSet()) {
			expr := s.GetSet()[key]
			fd, err := resolveFieldPath(state, key)
			if err != nil {
				bad("step %q: `set` key %q: %v", id, key, err)
				continue
			}
			ast, iss := senv.Compile(expr)
			if iss != nil && iss.Err() != nil {
				bad("step %q: `set[%s]` %q does not compile: %s",
					id, key, expr, celMessages(iss))
				continue
			}
			if !celTypeFits(ast.OutputType(), fd) {
				bad("step %q: `set[%s]` is %s but the state field %s is %s",
					id, key, ast.OutputType(), key, fieldType(fd))
			}
		}
	}

	// An edge predicate runs AFTER its `from` step, so it sees that step's
	// writes as well as everything written before it.
	for _, e := range p.GetEdges() {
		if e.GetWhen() == "" {
			continue
		}
		from := e.GetFrom()
		avail := writtenBefore(from)
		for f := range writesBy[from] {
			avail[f] = true
		}
		label := fmt.Sprintf("edge %s->%s", from, e.GetTo())
		checkReadsExpr(bad, state, avail, nil, whoWrites, label, e.GetWhen())
		ast, iss := stateOnly.Compile(e.GetWhen())
		if iss != nil && iss.Err() != nil {
			bad("%s: `when` %q does not compile: %s", label, e.GetWhen(), celMessages(iss))
			continue
		}
		if !ast.OutputType().IsExactType(cel.BoolType) {
			bad("%s: `when` is %s, and an edge predicate must be bool: the runner "+
				"takes the edge when it is true and has nothing to compare when it "+
				"is anything else", label, ast.OutputType())
		}
	}
	return out
}

// agentStepReplies maps an agent's Invoke tool FQN to that same agent's
// GetRun OUTPUT descriptor (garm.agent.v1.RunStatus) — what a step calling
// that FQN actually sees at `response`, per the note in
// lintWorkflowExpressionsWith above. Built once over the whole linted set and
// shared across every agent's A7 run, the same way toolIndex is.
//
// This is garm lint's version of agentd's catalogue.agentRepliesFrom
// (internal/catalogue/generation.go), and reads the same fact off the same
// two methods, but it does NOT reproduce that function's fixed point.
// agentd's build() iterates verifyGraph to a fixed point because it resolves
// a GENERATION: an agent can be REFUSED there (a bad guard, a bad prompt, a
// bad graph) and carry forward an older definition instead, which changes
// what agentRepliesFrom should hand a SIBLING step that invokes it on the
// next round — admission is the thing that can still change.
//
// garm lint has no admission step to iterate on. It sees a composed set of
// proto packages and answers one question about each agent in it: does its
// own declaration lint clean. There is no "refused, carry forward the
// previous version" here and no notion of a tentatively-live agent a sibling
// might need to see differently next round — every agent in `agents` is
// simply, structurally, either present in this set with an Invoke and a
// GetRun or it is not, and that fact does not change while this function
// runs. So one pass over `agents`, taken as given, is the whole of it; a
// fixed point here would be solving a problem — admission changing between
// rounds — that does not exist on this side of the fence.
func agentStepReplies(agents []Agent, tools map[string]Tool) map[string]protoreflect.MessageDescriptor {
	byInvoke := make(map[protoreflect.MethodDescriptor]protoreflect.MessageDescriptor, len(agents))
	for _, ag := range agents {
		if ag.Invoke != nil && ag.GetRun != nil {
			byInvoke[ag.Invoke] = ag.GetRun.Output()
		}
	}
	out := map[string]protoreflect.MessageDescriptor{}
	for fqn, t := range tools {
		if status, ok := byInvoke[t.Method]; ok {
			out[fqn] = status
		}
	}
	return out
}

// lintWorkflowInitial checks the `initial` block: its keys against the state
// message, its expressions against `input`, Invoke's request.
//
// `initial` is the ONLY place a caller's request enters the state, so a typo
// in one of these is a state field that is silently zero for the whole run —
// the same hole the dominance rule closes, one level earlier. Both halves need
// only the agent's own file, so this runs under PartialSet too.
func lintWorkflowInitial(a Agent, state protoreflect.MessageDescriptor, keys map[string]bool) []Diag {
	if len(keys) == 0 {
		return nil // A8 reports an empty `initial`; nothing to check here.
	}
	var out []Diag
	bad := func(format string, args ...any) {
		out = append(out, Diag{Rule: "A7", Path: string(a.FQN),
			Msg: fmt.Sprintf(format, args...)})
	}
	if a.Invoke == nil {
		// A1 reports the missing Invoke. Without its request type there is no
		// `input`, so the expressions cannot be checked at all.
		return nil
	}
	// `state` is deliberately absent from this environment: `initial` is what
	// creates the state, so there is nothing yet to read.
	env, err := celEnvFor([]celVar{{name: "input", md: a.Invoke.Input()}})
	if err != nil {
		bad("cannot build an environment over %s for `initial`: %v",
			a.Invoke.Input().FullName(), err)
		return out
	}
	for _, key := range sortedSet(keys) {
		expr := a.Policy.GetInitial()[key]
		fd, err := resolveFieldPath(state, key)
		if err != nil {
			bad("`initial` key %q: %v", key, err)
			continue
		}
		ast, iss := env.Compile(expr)
		if iss != nil && iss.Err() != nil {
			bad("`initial[%s]` %q does not compile against %s: %s",
				key, expr, a.Invoke.Input().FullName(), celMessages(iss))
			continue
		}
		if !celTypeFits(ast.OutputType(), fd) {
			bad("`initial[%s]` is %s but the state field %s is %s",
				key, ast.OutputType(), key, fieldType(fd))
		}
	}
	return out
}

// absentToolWarning is the one warning a one-directory run produces: A7 (and,
// since the tool index is the same one `lintStatePropagation` reads, A11)
// could not resolve the tools of some steps, because buf invokes the plugin
// per directory and they live elsewhere.
//
// A11 gets no separate warning of its own. This function's own case for ONE
// warning per agent rather than one per concern — a reader who skims N
// same-location findings as a repeat — applies exactly as much to a second
// function's warning sharing that location, so A11's gap is folded into this
// warning's enumeration instead of duplicating it.
//
// ONE per agent, not one per step, for two reasons. Every diagnostic here
// carries the same Path — the agent's FQN — so N of them are N same-location
// findings differing only in an interchangeable clause, which a reader is
// entitled to skim as a repeat. And A3, the rule this warning is modelled on,
// emits one per agent covering the whole allowlist rather than one per tool.
//
// The steps are listed in DECLARATION order, so the sentence is byte-identical
// across runs; the checks that did not run and the commands that do run them
// are stated once.
func absentToolWarning(a Agent, allowed map[string]bool, tools map[string]Tool,
	opts Options) []Diag {
	if !opts.PartialSet {
		return nil // with the whole set assembled there is nothing to defer to
	}
	var absent []string
	for _, s := range a.Policy.GetSteps() {
		// A step outside the allowlist is an error of its own, reported in the
		// step loop; it is not something another command would resolve.
		if !allowed[s.GetTool()] {
			continue
		}
		if _, ok := tools[s.GetTool()]; !ok {
			absent = append(absent, fmt.Sprintf("%s (%s)", s.GetId(), s.GetTool()))
		}
	}
	if len(absent) == 0 {
		return nil
	}
	return []Diag{{Rule: "A7", Path: string(a.FQN), Warn: true, Msg: fmt.Sprintf(
		"the tools of these steps are not in this run's input: %s. So `with` keys "+
			"against a tool's request, the type of each `with` value against the "+
			"field it feeds, `set` expressions against a tool's response, whether a "+
			"`set`'s state field is at least as protected as the response field it "+
			"reads there (A11), and which request fields are the runner's are NOT "+
			"checked here — this run sees one directory rather than the catalogue. "+
			"`garm lint` and `garm catalogue build` resolve every tool against the "+
			"whole tree and do check them. Everything answerable from this agent's "+
			"own declaration, the rule that a state field is read only where every "+
			"path has written it included, was checked and is an error above",
		strings.Join(absent, ", "))}}
}

// checkWithCompiles compiles a step's `with` expressions against `state` alone
// and reports the ones that do not compile.
//
// Reached only when the step's tool is absent under PartialSet. The type FIT of
// a value against the request field it feeds needs that tool; whether the
// expression compiles at all needs only the state message, which is in the
// agent's own file. A7's own principle puts the second on this side of the
// line, so a mistyped `state.nope` is refused on every `buf generate` and not
// only where the catalogue is assembled.
func checkWithCompiles(bad func(string, ...any), env *cel.Env,
	step string, with map[string]string) {
	for _, key := range sortedKeys(with) {
		expr := with[key]
		if _, iss := env.Compile(expr); iss != nil && iss.Err() != nil {
			bad("step %q: `with[%s]` %q does not compile: %s",
				step, key, expr, celMessages(iss))
		}
	}
}

// checkSetKeys resolves a step's `set` keys against the state message and
// nothing else. Split out because it is the one part of a step that a
// one-directory run can still answer when the step's TOOL is absent.
func checkSetKeys(bad func(string, ...any), state protoreflect.MessageDescriptor,
	step string, set map[string]string) {
	for _, key := range sortedKeys(set) {
		if _, err := resolveFieldPath(state, key); err != nil {
			bad("step %q: `set` key %q: %v", step, key, err)
		}
	}
}

// checkReads refuses a read of a state field that is not guaranteed written,
// over every expression at one site of one step.
func checkReads(bad func(string, ...any), state protoreflect.MessageDescriptor,
	avail, own map[string]bool, whoWrites func(string) []string,
	step, site string, exprs map[string]string) {
	for _, k := range sortedKeys(exprs) {
		checkReadsExpr(bad, state, avail, own, whoWrites,
			fmt.Sprintf("step %q `%s[%s]`", step, site, k), exprs[k])
	}
}

// checkReadsExpr refuses a read of a state field that is not guaranteed
// written on every path to here.
//
// It PARSES rather than greps: `state.note` appears inside string
// concatenation, comparisons, ternaries and function arguments, and a
// substring search would both miss nested uses and match a field name
// occurring in a string literal.
//
// A parse-only environment is deliberate. This half of A7 must run where the
// tool descriptors are absent, and finding the NAMES a read touches needs no
// types at all.
// `own` is the reading step's OWN writes, or nil at an edge, and exists only
// to tell one likely mistake from the general one.
func checkReadsExpr(bad func(string, ...any), state protoreflect.MessageDescriptor,
	avail, own map[string]bool, whoWrites func(string) []string, label, expr string) {
	env, err := cel.NewEnv()
	if err != nil {
		return
	}
	ast, iss := env.Parse(expr)
	if iss != nil && iss.Err() != nil {
		return // the typed compile reports a syntax error, with its own sentence
	}
	for _, field := range stateSelections(ast) {
		if _, err := resolveFieldPath(state, field); err != nil {
			continue // not a state field at all; the typed compile says so
		}
		if writtenCovers(avail, field) {
			continue
		}
		if writtenCovers(own, field) {
			// The mistake an author makes first, and the general sentence below
			// would tell them their own step does not dominate itself.
			bad("%s reads state.%s, which this same step's `set` writes — and both "+
				"`with` and `set` are evaluated against the state as it was BEFORE "+
				"this step ran, so the read sees the zero value rather than what "+
				"this step is about to write. Write the field in an earlier step, "+
				"or set it in `initial`", label, field)
			continue
		}
		who := whoWrites(field)
		if len(who) == 0 {
			bad("%s reads state.%s, which no step writes and `initial` does not "+
				"set, so it would hold its zero value on every path. Set it in "+
				"`initial`, or write it in a step that runs before this one",
				label, field)
			continue
		}
		bad("%s reads state.%s, which is written by %v — and at least one path "+
			"to here does not pass through %v, so the field would silently hold "+
			"its zero value. Either move the read onto a branch those steps "+
			"dominate, or set the field in `initial`", label, field, who, who)
	}
}

// stateSelections returns the state field paths an expression reads.
//
// It walks the parsed tree for Select nodes whose operand chain bottoms out at
// the identifier `state`, and reports the LONGEST chain: `state.amount.minor`
// is one read of `amount.minor`, not also a read of `amount`. Presence tests
// (`has(state.x)`) are reads like any other — the rule is one sentence and
// stays one sentence.
//
// A thin wrapper over rootedSelections: A11 needs the identical walk rooted
// at `response` instead of `state`, so the walk itself is parameterised and
// this keeps its name and behaviour for A7's callers and tests.
func stateSelections(a *cel.Ast) []string {
	return rootedSelections(a, "state")
}

// statePath returns the dotted field path of a selection chain rooted at the
// identifier `state`, or "" for any other chain. See stateSelections.
func statePath(e celast.Expr) string {
	return rootedPath(e, "state")
}

// rootedSelections returns the field paths an expression reads off the
// variable named root — the LONGEST chain per read, as stateSelections
// documents. `state` and `response` are both plain CEL variables, so the same
// walk answers both: A7 asks it about `state`, A11 about `response`.
func rootedSelections(a *cel.Ast, root string) []string {
	if a == nil || a.NativeRep() == nil {
		return nil
	}
	// A Select that is another Select's operand is a prefix of a longer chain,
	// so it is not a read of its own.
	inner := map[int64]bool{}
	paths := map[int64]string{}
	var order []int64
	celast.PostOrderVisit(a.NativeRep().Expr(), celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		inner[e.AsSelect().Operand().ID()] = true
		if path := rootedPath(e, root); path != "" {
			paths[e.ID()] = path
			order = append(order, e.ID())
		}
	}))
	var out []string
	seen := map[string]bool{}
	for _, id := range order {
		if inner[id] {
			continue
		}
		if p := paths[id]; !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// rootedPath returns the dotted field path of a selection chain rooted at the
// identifier named root, or "" for any other chain — including one rooted at
// a different identifier, or one that is not a plain selection chain at all
// (a call, an operator, a literal). That last case is exactly how A11 tells a
// direct `response.matches` apart from `size(response.matches)`: called on an
// expression's OWN top node, a non-empty result means the whole expression is
// nothing but the selection.
func rootedPath(e celast.Expr, root string) string {
	var segs []string
	for e.Kind() == celast.SelectKind {
		sel := e.AsSelect()
		segs = append(segs, sel.FieldName())
		e = sel.Operand()
	}
	if e.Kind() != celast.IdentKind || e.AsIdent() != root {
		return ""
	}
	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}
	return strings.Join(segs, ".")
}

// writtenCovers reports whether the set of written paths makes a read of
// `read` safe.
func writtenCovers(written map[string]bool, read string) bool {
	for w := range written {
		if pathCovers(w, read) {
			return true
		}
	}
	return false
}

// pathCovers reports whether a write of `written` puts a value where a read of
// `read` looks. Either may be a segment-prefix of the other: writing `amount`
// fills everything a read below it sees, and writing `amount.minor_units`
// constructs `amount`, so a read of either reaches something a step wrote.
//
// What it refuses is the case A7 exists for — nothing on this path touched
// that subtree at all, so the read returns the zero value. Presence WITHIN a
// written message is not this rule's granularity and cannot be: writing
// `amount` from a response says nothing about which of its own fields the tool
// filled.
func pathCovers(written, read string) bool {
	return written == read ||
		strings.HasPrefix(read, written+".") ||
		strings.HasPrefix(written, read+".")
}

// celVar is one variable declaration: a name and the message type it holds.
type celVar struct {
	name string
	md   protoreflect.MessageDescriptor
}

// celEnvFor builds a CEL environment declaring each variable as its message
// type, over a type registry walked from every one of those messages' files.
//
// The descriptors come from the tree being linted, not from anything this
// binary links, so the provider is built from the file descriptors — exactly
// as guardEnv does for A4. The IMPORT walk is what makes a
// google.protobuf.Timestamp inside a state message resolve rather than
// becoming an unknown type the moment an expression touches it.
func celEnvFor(vars []celVar) (*cel.Env, error) {
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	opts := make([]cel.EnvOption, 0, len(vars)+2)
	opts = append(opts, cel.CustomTypeAdapter(reg), cel.CustomTypeProvider(reg))
	for _, v := range vars {
		if err := registerFileAndImports(reg, v.md.ParentFile(), seen); err != nil {
			return nil, err
		}
		opts = append(opts, cel.Variable(v.name, cel.ObjectType(string(v.md.FullName()))))
	}
	return cel.NewEnv(opts...)
}

// stateEnv declares `state` plus any extra variables — `response`, at a
// step's `set` — over a registry built from every one of those messages'
// files and their imports.
func stateEnv(state protoreflect.MessageDescriptor,
	extra map[string]protoreflect.MessageDescriptor) (*cel.Env, error) {
	vars := make([]celVar, 0, len(extra)+1)
	vars = append(vars, celVar{name: "state", md: state})
	names := make([]string, 0, len(extra))
	for n := range extra {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic, so a failure reproduces
	for _, n := range names {
		vars = append(vars, celVar{name: n, md: extra[n]})
	}
	return celEnvFor(vars)
}

// resolveFieldPath walks a dotted path, so `amount.minor_units` reaches a
// nested field without an author constructing a submessage in an expression.
func resolveFieldPath(md protoreflect.MessageDescriptor, path string) (protoreflect.FieldDescriptor, error) {
	if path == "" {
		return nil, fmt.Errorf("is empty; name a field")
	}
	parts := strings.Split(path, ".")
	cur := md
	for i, part := range parts {
		fd := cur.Fields().ByName(protoreflect.Name(part))
		if fd == nil {
			return nil, fmt.Errorf("%s has no field %q", cur.FullName(), part)
		}
		if i == len(parts)-1 {
			return fd, nil
		}
		if fd.IsMap() || fd.IsList() {
			return nil, fmt.Errorf("%s.%s is repeated, and a path may not index "+
				"into it, so %q cannot continue", cur.FullName(), part, path)
		}
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return nil, fmt.Errorf("%s.%s is not a message, so %q cannot continue",
				cur.FullName(), part, path)
		}
		cur = fd.Message()
	}
	return nil, fmt.Errorf("empty path")
}

// isRunnerOwned reports whether a request field is the RUNNER's to set.
//
// One definition, read off the annotation: a top-level request field whose own
// field policy says `source: SOURCE_RUNNER`. That is exactly what agentd's
// catalogue reader computes (catalogue.runnerFieldsOf) and what agentd's
// stripRunnerFields then removes from the schema it shows a model, and what
// L34 holds to a name the runner has a rule for. A hard-coded list of names
// here would be a second definition that drifts from the protos the first time
// one is added.
//
// Top level, and the field's OWN policy: a message default is not consulted,
// for the same reason it is not there — a default that made every field the
// runner's would describe a request nobody could send, and L34 refuses it. The
// path's ROOT segment is what is judged, so `idempotency_key.anything` is
// refused too.
func isRunnerOwned(tool Tool, path string) bool {
	root, _, _ := strings.Cut(path, ".")
	fd := tool.Method.Input().Fields().ByName(protoreflect.Name(root))
	if fd == nil {
		return false // resolveFieldPath already reported that it is not a field
	}
	return policy.FieldPolicyOf(fd, nil).GetSource() == toolv1.FieldPolicy_SOURCE_RUNNER
}

// celTypeFits reports whether a value of CEL type t may be written to fd.
//
// Deliberately CONSERVATIVE: an exact match, and only the widenings protobuf
// marshalling genuinely performs. A permissive version here would put the
// failure back at run time, inside a governed call, which is the thing A7
// exists to prevent. Two consequences worth stating, because they are the ones
// that look like gaps:
//
//   - A dyn-typed expression FAILS. The checker producing dyn means it could
//     not determine the type, and that is precisely where a run-time
//     marshalling failure lives.
//   - Integer WIDTH is not checked, because CEL has no int32: every CEL
//     integer is an int64 and every CEL unsigned is a uint64, so `int` into an
//     int32 field is the only thing an author can write and the range check is
//     the runner's. Signedness IS checked — int into a uint64 field is
//     refused, since half its range has no representation.
func celTypeFits(t *cel.Type, fd protoreflect.FieldDescriptor) bool {
	if t == nil || fd == nil {
		return false
	}
	switch {
	case fd.IsMap():
		if t.Kind() != types.MapKind {
			return false
		}
		ps := t.Parameters()
		return len(ps) == 2 && celValueFits(ps[0], fd.MapKey()) && celValueFits(ps[1], fd.MapValue())
	case fd.IsList():
		if t.Kind() != types.ListKind {
			return false
		}
		ps := t.Parameters()
		// fd.Kind() on a repeated field is its ELEMENT's kind, so the same
		// judgement applies to the element type.
		return len(ps) == 1 && celValueFits(ps[0], fd)
	default:
		return celValueFits(t, fd)
	}
}

// celValueFits judges one value against a field's type, ignoring cardinality.
func celValueFits(t *cel.Type, fd protoreflect.FieldDescriptor) bool {
	if t == nil {
		return false
	}
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return t.Kind() == types.BoolKind
	case protoreflect.StringKind:
		return t.Kind() == types.StringKind
	case protoreflect.BytesKind:
		return t.Kind() == types.BytesKind
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return t.Kind() == types.IntKind
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return t.Kind() == types.UintKind
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		// An integer into a float field is the one numeric widening protobuf
		// JSON genuinely performs.
		return t.Kind() == types.DoubleKind || t.Kind() == types.IntKind ||
			t.Kind() == types.UintKind
	case protoreflect.EnumKind:
		// CEL represents a proto enum as an int, so an int is the only thing
		// an expression can produce for one. Which values are declared is not
		// knowable from a type.
		return t.Kind() == types.IntKind
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return celMessageFits(t, fd.Message())
	}
	return false
}

// celMessageFits judges a value against a message-typed field.
//
// The well-known types are named individually rather than matched by name
// prefix, because each has its own CEL representation and a rule that guessed
// would be exactly the permissive version this file argues against.
func celMessageFits(t *cel.Type, md protoreflect.MessageDescriptor) bool {
	if md == nil {
		return false
	}
	if t.Kind() == types.NullTypeKind {
		return true // null clears a message field; protobuf JSON says so
	}
	name := string(md.FullName())
	switch name {
	case "google.protobuf.Timestamp":
		return t.Kind() == types.TimestampKind
	case "google.protobuf.Duration":
		return t.Kind() == types.DurationKind
	case "google.protobuf.BoolValue":
		return t.Kind() == types.BoolKind
	case "google.protobuf.StringValue":
		return t.Kind() == types.StringKind
	case "google.protobuf.BytesValue":
		return t.Kind() == types.BytesKind
	case "google.protobuf.Int32Value", "google.protobuf.Int64Value":
		return t.Kind() == types.IntKind
	case "google.protobuf.UInt32Value", "google.protobuf.UInt64Value":
		return t.Kind() == types.UintKind
	case "google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return t.Kind() == types.DoubleKind || t.Kind() == types.IntKind ||
			t.Kind() == types.UintKind
	case "google.protobuf.Struct":
		return t.Kind() == types.MapKind
	case "google.protobuf.ListValue":
		return t.Kind() == types.ListKind
	case "google.protobuf.Value":
		// A Value holds any JSON value, so anything with a determinate type
		// fits. Dyn does not: it means the checker could not tell.
		switch t.Kind() {
		case types.BoolKind, types.BytesKind, types.DoubleKind, types.IntKind,
			types.UintKind, types.StringKind, types.ListKind, types.MapKind,
			types.NullTypeKind, types.StructKind:
			return true
		}
		return false
	}
	return t.Kind() == types.StructKind && t.TypeName() == name
}

// fieldType renders a field's type the way a schema author wrote it, because
// "message" tells an author nothing about which message was wanted.
func fieldType(fd protoreflect.FieldDescriptor) string {
	switch {
	case fd.IsMap():
		return fmt.Sprintf("map<%s, %s>", fieldType(fd.MapKey()), fieldType(fd.MapValue()))
	case fd.IsList():
		return "repeated " + scalarTypeName(fd)
	default:
		return scalarTypeName(fd)
	}
}

func scalarTypeName(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return string(fd.Message().FullName())
	case protoreflect.EnumKind:
		return string(fd.Enum().FullName())
	default:
		return fd.Kind().String()
	}
}

// sortedKeys and sortedSet make every diagnostic's ORDER deterministic: `with`
// and `set` are proto maps, and a rule that reports in map order reports in a
// different order on every run, which no golden file can hold.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// lintStatePropagation covers A11: a state field is at least as protected as
// what it is written from.
//
// GetState's output IS the state message (A8), and garmd projects it per
// caller like any other governed reply — so a `set` that lifts a value out of
// a tool's RESPONSE and into a state field is a disclosure of that value at
// whatever grade the state field claims. The bank's own screening schema made
// this judgement in prose once already: `requires_review` is graded one below
// `matches`, with a comment saying a routing decision is safe to show a
// supervisor who never sees the names themselves. `derives` is that judgement
// written so a machine can check it, and this rule is the check.
//
// Only `response` reads matter here, not `state` reads: a `set` expression
// copying one state field into another moves nothing that was not already
// published by whichever earlier `set` put it there, and that earlier write is
// where this rule already looked at it.
//
// A field written by a DIRECT selection (`flag: response.flagged`) is compared
// exactly: the state field's clearance must be at least the source's, per
// `contracts/policy.Allows`, and its compartments must be a superset. An
// expression that reads `response` without being one — `size(response.matches)`
// — changes the shape of what is disclosed in a way this rule cannot size up
// field-for-field, so it demands a `derives` annotation instead of attempting
// the comparison. Either way, a `derives` with an empty reason is refused: an
// escape hatch that asks for nothing is not an escape hatch, it is silence.
//
// A reasoned `derives` exempts only the source its `from` NAMES, never every
// read a `set` might perform. Without that check one field's legitimate,
// reviewed downgrade would silently cover every other field the same `set`
// happened to also read — including one the annotation never mentioned and
// nobody reviewed the sensitivity of.
//
// Silent when the source carries no policy at all — a field with neither its
// own `field_policy` nor a message default has nothing to propagate, and
// inventing a classification nobody declared would be a rule the schema
// author cannot act on.
func lintStatePropagation(a Agent, tools map[string]Tool) []Diag {
	p := a.Policy
	if p.GetMode() != agentv1.Mode_MODE_WORKFLOW {
		return nil
	}
	state := a.StateMessage
	if state == nil {
		return nil // A8 already reports a workflow agent with no GetState
	}
	var out []Diag
	for _, s := range p.GetSteps() {
		if len(s.GetSet()) == 0 {
			continue
		}
		tool, ok := tools[s.GetTool()]
		if !ok {
			// A7 (or its PartialSet warning) already says this step's tool is
			// unresolved; there is no response descriptor here to compare
			// against.
			continue
		}
		resp := tool.Method.Output()
		env, err := stateEnv(state, map[string]protoreflect.MessageDescriptor{"response": resp})
		if err != nil {
			continue // A7 already reports environment failures for this step
		}
		for _, key := range sortedKeys(s.GetSet()) {
			out = append(out, checkStatePropagation(a, s.GetId(), key, s.GetSet()[key],
				state, resp, env)...)
		}
	}
	return out
}

// checkStatePropagation is A11 for one `set` entry.
func checkStatePropagation(a Agent, step, key, expr string,
	state, resp protoreflect.MessageDescriptor, env *cel.Env) []Diag {
	stateFD, err := resolveFieldPath(state, key)
	if err != nil {
		return nil // A7 already reports a `set` key that does not resolve
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil // A7 already reports an expression that does not compile
	}
	reads := rootedSelections(ast, "response")
	if len(reads) == 0 {
		return nil // nothing from `response` reaches this field; nothing to check
	}
	direct := rootedPath(ast.NativeRep().Expr(), "response") != ""

	// Gather the policy of every response field the expression touches. Only
	// a GOVERNED source makes this a disclosure at all.
	worst := toolv1.Clearance_CLEARANCE_UNSPECIFIED
	need := map[string]bool{}
	var sources []string
	governed := false
	for _, read := range reads {
		srcFD, err := resolveFieldPath(resp, read)
		if err != nil {
			continue // not a real response field; the typed compile already said so
		}
		fp := effectiveFieldPolicy(srcFD)
		if fp == nil {
			continue
		}
		governed = true
		sources = append(sources, fmt.Sprintf("%s.%s (%s)", resp.FullName(), read, fp.GetRead()))
		if fp.GetRead() > worst {
			worst = fp.GetRead()
		}
		for _, c := range fp.GetCompartments() {
			need[c] = true
		}
	}
	if !governed {
		return nil // the source carries no policy; there is nothing to propagate
	}
	sort.Strings(sources)
	from := strings.Join(sources, ", ")

	bad := func(format string, args ...any) Diag {
		return Diag{Rule: "A11", Path: string(a.FQN), Msg: fmt.Sprintf(format, args...)}
	}
	derivesEscape := func() string {
		return fmt.Sprintf("declare (garm.agent.v1.derives) = { from: %q reason: \"...\" } "+
			"on %s if the downgrade is deliberate", "response."+reads[0], key)
	}

	if dv := fieldDerives(stateFD); dv != nil {
		if dv.GetReason() == "" {
			return []Diag{bad("step %q: `set[%s]` state field %s declares "+
				"(garm.agent.v1.derives) with an empty reason. It is written from %s, "+
				"which GetState would otherwise publish at a weaker grade — and a "+
				"downgrade whose escape hatch is silence is not a rule. Say why "+
				"publishing %s at a lower grade is safe",
				step, key, key, from, key)}
		}
		// A reasoned `derives` exempts only the source it NAMES. Without this
		// check, one field's legitimate downgrade (`match_count` from
		// `response.matches`) would silently exempt every OTHER field this
		// `set` might read from — including a direct copy of an unrelated,
		// more sensitive field the annotation never mentioned.
		named := false
		for _, read := range reads {
			if dv.GetFrom() == "response."+read {
				named = true
				break
			}
		}
		if !named {
			return []Diag{bad("step %q: `set[%s]` state field %s declares "+
				"(garm.agent.v1.derives) with from: %q, but this `set` is written "+
				"from %s — a derives annotation exempts only the source it names, "+
				"so it does not cover this one. Point `from` at what is actually "+
				"read, or remove the annotation if this field's own grade should "+
				"apply",
				step, key, key, dv.GetFrom(), from)}
		}
		return nil // a declared, reasoned downgrade naming this exact source
	}

	if !direct {
		return []Diag{bad("step %q: `set[%s]` is written from %s through an expression "+
			"that is not a direct field selection, so A11 cannot compare its clearance "+
			"and compartments field-for-field; %s",
			step, key, from, derivesEscape())}
	}

	stateFP := effectiveFieldPolicy(stateFD)
	stateRead := stateFP.GetRead()
	have := map[string]bool{}
	for _, c := range stateFP.GetCompartments() {
		have[c] = true
	}

	var diags []Diag
	if !policy.Allows(stateRead, worst) {
		diags = append(diags, bad("step %q: `set[%s]` is written from %s; the state "+
			"field %s reads at %s, which is weaker. GetState publishes this message, so "+
			"a state field must be at least as protected as what it is written from — "+
			"raise %s to %s, or %s",
			step, key, from, key, stateRead, key, worst, derivesEscape()))
	}
	var missing []string
	for c := range need {
		if !have[c] {
			missing = append(missing, c)
		}
	}
	sort.Strings(missing)
	for _, c := range missing {
		diags = append(diags, bad("step %q: `set[%s]` is written from %s, which carries "+
			"the %q compartment; the state field %s does not. GetState publishes this "+
			"message, so a state field must be at least as protected as what it is "+
			"written from — add %q to %s, or %s",
			step, key, from, c, key, c, key, derivesEscape()))
	}
	return diags
}

// effectiveFieldPolicy returns fd's own field_policy, or its message's
// default_field_policy, or nil when neither is declared — the same
// resolution policy.Compile itself performs field by field.
func effectiveFieldPolicy(fd protoreflect.FieldDescriptor) *toolv1.FieldPolicy {
	if fd == nil {
		return nil
	}
	def := policy.MessageDefaultPolicy(fd.ContainingMessage())
	return policy.FieldPolicyOf(fd, def)
}

// fieldDerives returns the (garm.agent.v1.derives) annotation on fd, or nil
// when it carries none.
func fieldDerives(fd protoreflect.FieldDescriptor) *agentv1.DerivedValue {
	if fd == nil {
		return nil
	}
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || !proto.HasExtension(opts, agentv1.E_Derives) {
		return nil
	}
	dv, _ := proto.GetExtension(opts, agentv1.E_Derives).(*agentv1.DerivedValue)
	return dv
}
