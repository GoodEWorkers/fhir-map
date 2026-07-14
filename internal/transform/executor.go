package transform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform/fhirpath"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// runRule executes one rule at the given dependent-call depth.
func (e *Engine) runRule(ctx context.Context, rule *structuremap.Rule, parentScope *scope, depth int) error {
	if len(rule.Source) == 0 {
		// Pure-target rules are rare but legal — they produce a constant
		// target without consulting the source. Just execute the targets
		// once with whatever the parent scope already holds.
		return e.executeTargets(ctx, rule, parentScope, depth)
	}

	// Group-scoped `let` rules carry a source binding with no targets, rules,
	// or dependents. The FML parser lowers `let name = expr;` into this shape.
	// The bound value must outlive the rule iteration so subsequent rules can
	// reference %name — write it to the root frame via setRoot.
	//
	// Empty-binding contract: a `let` whose expression yields zero bindings
	// binds nil to the variable rather than silently skipping the write.
	// Skipping would leave %name unbound (or carrying a stale parent-frame
	// value); subsequent references would then fail with a confusing
	// variable-not-found at a site far from the actual `let`. Binding nil
	// makes the "no match" semantics explicit and traceable.
	if len(rule.Source) == 1 && len(rule.Target) == 0 && len(rule.Rule) == 0 && len(rule.Dependent) == 0 &&
		rule.Source[0].Variable != "" {
		b, err := e.evalSource(&rule.Source[0], parentScope)
		if err != nil {
			return fmt.Errorf("source %d: %w", 0, err)
		}
		switch len(b) {
		case 0:
			parentScope.setRoot(rule.Source[0].Variable, nil)
		case 1:
			parentScope.setRoot(rule.Source[0].Variable, b[0])
		default:
			parentScope.setRoot(rule.Source[0].Variable, b)
		}
		return nil
	}

	// Evaluate every source independently and iterate the cartesian product.
	// If any source produces an empty binding the whole rule no-ops.
	// source.min cardinality enforcement inside evalSource catches required sources.
	bindings := make([][]any, len(rule.Source))
	for i := range rule.Source {
		b, err := e.evalSource(&rule.Source[i], parentScope)
		if err != nil {
			return fmt.Errorf("source %d: %w", i, err)
		}
		if len(b) == 0 {
			return nil
		}
		bindings[i] = b
	}
	return e.iterateSourceProduct(ctx, rule, bindings, 0, scopeFork(parentScope), depth)
}

// iterateSourceProduct walks the cartesian product of source bindings
// recursively. At each depth it forks a scope, binds Source[depth].
// Variable to the current iteration's value, and recurses. At the
// deepest level it fires executeTargets once with all source variables
// bound on the chain.
//
// srcDepth is the source-product iteration depth (independent of dependent
// call depth); recursionDepth is the dependent-call depth passed through.
func (e *Engine) iterateSourceProduct(
	ctx context.Context,
	rule *structuremap.Rule,
	bindings [][]any,
	srcDepth int,
	sc *scope,
	recursionDepth int,
) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if srcDepth == len(rule.Source) {
		return e.executeTargets(ctx, rule, sc, recursionDepth)
	}
	src := &rule.Source[srcDepth]
	for _, val := range bindings[srcDepth] {
		child := scopeFork(sc)
		if src.Variable != "" {
			child.set(src.Variable, val)
		}
		if err := e.iterateSourceProduct(ctx, rule, bindings, srcDepth+1, child, recursionDepth); err != nil {
			return err
		}
	}
	return nil
}

// evalSource evaluates a source's path expression against its declared
// context variable. Returns the bound collection, or an error if the
// context isn't in scope.
//
// A blank Element is "the whole context value" (FHIR mapping convention).
func (e *Engine) evalSource(src *structuremap.Source, sc *scope) ([]any, error) {
	root, ok := sc.get(src.Context)
	if !ok {
		return nil, fmt.Errorf("source context variable %q not bound", src.Context)
	}
	env := sc.envSnapshot()

	// Empty Element means [root]; the condition/listMode/default/check/cardinality
	// pipeline still runs uniformly so enforcement fires regardless of navigation.
	var binding []any
	if src.Element == "" {
		binding = []any{root}
	} else {
		b, err := fhirpath.EvalIn(src.Element, root, env)
		if err != nil {
			return nil, fmt.Errorf("evaluating source.element %q: %w", src.Element, err)
		}
		binding = b
	}
	if src.Condition != "" {
		// Apply the `where`-style condition, dropping items that don't evaluate truthy.
		var kept []any
		for _, b := range binding {
			res, err := fhirpath.EvalIn(src.Condition, b, env)
			if err != nil {
				return nil, fmt.Errorf("evaluating source.condition %q: %w", src.Condition, err)
			}
			if truthyFP(res) {
				kept = append(kept, b)
			}
		}
		binding = kept
	}
	if src.ListMode != "" {
		filtered, err := applySourceListMode(binding, src.ListMode)
		if err != nil {
			return nil, err
		}
		binding = filtered
	}
	// defaultValue fills in for an empty binding; done BEFORE check and cardinality
	// so a configured default counts toward min satisfaction.
	if len(binding) == 0 && len(src.DefaultValue) > 0 {
		var dv any
		if err := json.Unmarshal(src.DefaultValue, &dv); err != nil {
			return nil, fmt.Errorf("source.defaultValue: invalid JSON: %w", err)
		}
		binding = []any{dv}
	}
	// check is a MUST-be-true assertion. Unlike condition (which filters),
	// a false check fails loud so invalid mappings don't silently produce wrong output.
	// Only the check expression (public contract) is in the error detail—never the
	// binding value, which may contain PHI.
	if src.Check != "" {
		for _, b := range binding {
			res, err := fhirpath.EvalIn(src.Check, b, env)
			if err != nil {
				return nil, fmt.Errorf("evaluating source.check %q: %w", src.Check, err)
			}
			if !truthyFP(res) {
				return nil, fmt.Errorf("%w: %s", ErrCheckFailed, src.Check)
			}
		}
	}
	// Enforce cardinality: min defaults to 0; max is an integer or "*" (unbounded).
	if err := enforceSourceCardinality(src, len(binding)); err != nil {
		return nil, err
	}
	return binding, nil
}

// enforceSourceCardinality validates the post-filter binding count
// against Source.Min / Source.Max (M6.Fix.1). Min is *int (defaults to 0
// when nil). Max is a wire string: "*" means unbounded, integer literals
// cap the count, empty string is no upper limit.
func enforceSourceCardinality(src *structuremap.Source, count int) error {
	minVal := 0
	if src.Min != nil {
		minVal = *src.Min
	}
	if count < minVal {
		return fmt.Errorf("source.min=%d not satisfied: got %d binding(s)", minVal, count)
	}
	if src.Max == "" || src.Max == "*" {
		return nil
	}
	maxVal, err := strconv.Atoi(src.Max)
	if err != nil {
		return err
	}
	if count > maxVal {
		return fmt.Errorf("source.max=%d exceeded: got %d binding(s)", maxVal, count)
	}
	return nil
}

// applySourceListMode trims a source binding collection per the FHIR
// Mapping Language sourceListMode (M6.5). Mode codes use either the R5
// hyphenated wire-format ("not-first", "only-one") or the FML text-form
// underscores ("not_first", "only_one"); both are accepted.
//
// only_one is a hard error when the binding has more than one element,
// matching HAPI's executor — silent truncation would mask the data-
// shape bug the marker is supposed to catch.
func applySourceListMode(binding []any, mode string) ([]any, error) {
	switch mode {
	case "first":
		if len(binding) == 0 {
			return nil, nil
		}
		return binding[:1], nil
	case "last":
		if len(binding) == 0 {
			return nil, nil
		}
		return binding[len(binding)-1:], nil
	case "not_first", "not-first":
		if len(binding) <= 1 {
			return nil, nil
		}
		return binding[1:], nil
	case "not_last", "not-last":
		if len(binding) <= 1 {
			return nil, nil
		}
		return binding[:len(binding)-1], nil
	case "only_one", "only-one":
		if len(binding) > 1 {
			return nil, fmt.Errorf("source.listMode = only_one but binding has %d elements", len(binding))
		}
		return binding, nil
	}
	return binding, nil
}

// executeTargets iterates target[] in declaration order. Each target's
// `context` must reference a value already in scope; the executor writes
// into that container by `element` name.
//
// After targets run, the rule's nested `Rule[]` execute in the same scope
// (M5d/e), then any `Dependent[]` declarations invoke the named group with
// the supplied parameters bound as its inputs (M5e).
//
// depth is the current dependent-call depth, threaded through from runDependent
// so self-recursive and mutually-recursive chains accumulate correctly.
func (e *Engine) executeTargets(ctx context.Context, rule *structuremap.Rule, sc *scope, depth int) error {
	for ti := range rule.Target {
		if err := e.executeTarget(ctx, &rule.Target[ti], sc); err != nil {
			return fmt.Errorf("target %d: %w", ti, err)
		}
	}
	// Execute nested rules and dependents in the same scope.
	for ri := range rule.Rule {
		if err := e.runRule(ctx, &rule.Rule[ri], sc, depth); err != nil {
			return fmt.Errorf("nested rule %q: %w", rule.Rule[ri].Name, err)
		}
	}
	for di := range rule.Dependent {
		if err := e.runDependent(ctx, &rule.Dependent[di], sc, depth); err != nil {
			return fmt.Errorf("dependent %q: %w", rule.Dependent[di].Name, err)
		}
	}
	return nil
}

// runDependent resolves a Dependent invocation: looks the named group up on
// the StructureMap (and any resolved imports) and runs it with positional
// parameters bound as its inputs.
//
// depth is the current dependent-call depth. It is incremented BEFORE looking
// up and entering the group so self-recursive and mutually-recursive chains
// accumulate correctly. Compared with `>=` so the MaxGroupRecursionDepth-th
// invocation is the one that trips ErrRecursionLimit (AC-4 wording: "5 or
// more nested invocations").
//
// Each parameter typically references a variable bound by the enclosing
// rule (Source.Variable or Target.Variable). The dependent group sees only
// its declared inputs — it can't reach back into the parent scope. That
// matches the FHIR spec's group-as-function semantics.
//
// extends runs parent rules inline and does NOT increment depth — only
// dependent→dependent chains count toward the recursion cap.
func (e *Engine) runDependent(ctx context.Context, dep *structuremap.Dependent, parentScope *scope, depth int) error {
	// Recursion cap: increment first, then check.
	depth++
	if depth >= MaxGroupRecursionDepth {
		return fmt.Errorf("%w: group %q (depth %d)", ErrRecursionLimit, dep.Name, depth)
	}

	// When dep.MapURL is set, resolve via MapResolver; when empty, lookup is local
	// (against current map's groups + imports). A parser that emitted empty MapURL
	// for `then map "<url>"` would silently downgrade to local lookup—the grammar
	// rejects empty quoted URLs to prevent this.
	var (
		group        *structuremap.Group
		childMap     *structuremap.StructureMap
		childImports []*structuremap.StructureMap
	)
	if dep.MapURL != "" {
		if e.mapResolver == nil {
			return fmt.Errorf("%w: map %q (canonical-URL lookup requested but no map resolver wired)", ErrMapNotFound, dep.MapURL)
		}
		impMap, err := e.mapResolver.FindByURL(ctx, dep.MapURL, "")
		if err != nil {
			// Distinguish missing-resource from transient errors so the handler
			// does not misclassify DB outages / cancellations as 422 not-found.
			if errors.Is(err, structuremap.ErrNotFound) {
				return fmt.Errorf("%w: map %q", ErrMapNotFound, dep.MapURL)
			}
			return fmt.Errorf("resolve map %q: %w", dep.MapURL, err)
		}
		// Defensive nil check: a misbehaving MapResolver returning (nil, nil)
		// would panic the iteration below; surface a clear ErrMapNotFound
		// instead of a nil-deref.
		if impMap == nil {
			return fmt.Errorf("%w: map %q (resolver returned nil)", ErrMapNotFound, dep.MapURL)
		}
		for i := range impMap.Group {
			if impMap.Group[i].Name == dep.Name {
				group = &impMap.Group[i]
				break
			}
		}
		if group == nil {
			return fmt.Errorf("%w: group %q in map %q", ErrMapNotFound, dep.Name, dep.MapURL)
		}
		// The child scope must expose impMap's own groups + transitive
		// imports so plain `dependent` calls inside the loaded group resolve
		// against impMap (not the caller's map). resolveImports is cycle-safe.
		childMap = impMap
		impImports, err := e.resolveImports(ctx, impMap)
		if err != nil {
			return err
		}
		childImports = impImports
	} else {
		group = parentScope.lookupGroupAcrossImports(dep.Name)
		if group == nil {
			// When imports are declared but no MapResolver is wired, imported maps aren't
			// loaded; a local lookup of a group from an import would silently miss.
			// Surface the wiring gap so operators can act on it.
			root := parentScope.root().sm
			if root != nil && len(root.Import) > 0 && e.mapResolver == nil {
				return fmt.Errorf("%w: group %q (imports configured but no map resolver wired)", ErrMapNotFound, dep.Name)
			}
			return fmt.Errorf("%w: group %q", ErrMapNotFound, dep.Name)
		}
		childMap = parentScope.root().sm
		childImports = parentScope.root().imports
	}

	if len(dep.Parameter) != len(group.Input) {
		return fmt.Errorf("dependent %q: passed %d arguments, group declares %d inputs",
			dep.Name, len(dep.Parameter), len(group.Input))
	}

	// Build a fresh scope rooted on the map that actually owns the group
	// (so dependents/extends inside the group resolve correctly).
	child := newScope()
	child.setMap(childMap)
	child.setImports(childImports)
	for i, in := range group.Input {
		val, err := resolveParameter([]structuremap.Parameter{dep.Parameter[i]}, parentScope)
		if err != nil {
			return fmt.Errorf("argument %d (%s): %w", i, in.Name, err)
		}
		child.set(in.Name, val)
	}

	// Inherit parent rules before running child's own rules. Look up parent in
	// the CHILD's scope so cross-map `extends` resolves against the loaded map.
	if group.Extends != "" {
		parent := child.lookupGroupAcrossImports(group.Extends)
		if parent == nil {
			return fmt.Errorf("%w: parent group %q", ErrMapNotFound, group.Extends)
		}
		for i := range parent.Rule {
			if err := e.runRule(ctx, &parent.Rule[i], child, depth); err != nil {
				return fmt.Errorf("rule %q (inherited from %s): %w", parent.Rule[i].Name, group.Extends, err)
			}
		}
	}
	for i := range group.Rule {
		if err := e.runRule(ctx, &group.Rule[i], child, depth); err != nil {
			return fmt.Errorf("rule %q: %w", group.Rule[i].Name, err)
		}
	}
	return nil
}

// executeTarget applies a single target action: handles scope plumbing (context
// resolution, target write, variable binding) and delegates to applyTransform.
func (e *Engine) executeTarget(ctx context.Context, t *structuremap.Target, sc *scope) error {
	ctxVal, ok := sc.get(t.Context)
	if !ok {
		// HAPI-compat: a target whose context variable was never bound
		// (because an earlier source-driven rule didn't match) is treated
		// as a no-op rather than a hard failure. This lets best-effort
		// transforms produce a partial target when upstream source data
		// is sparse, matching the permissive behaviour of HAPI's executor.
		return nil
	}
	holder, ok := ctxVal.(map[string]any)
	if !ok {
		// Same permissive logic: if the variable resolves to something
		// non-map (e.g. a string from a `copy` chain), skip rather than
		// hard-fail. Subsequent rules in the same group still run.
		return nil
	}

	// Target listMode `share <ruleId>`: collate successive firings into ONE
	// shared instance (HAPI's sharedVars). If an earlier firing already created
	// it, rebind the variable to that instance and skip re-creating/re-writing
	// the element, so later targets (e.g. `n.given = g`) accumulate into it.
	sharing := t.ListRuleId != "" && hasListMode(t.ListMode, "share")
	if sharing {
		if shared, ok := sc.getShared(t.ListRuleId); ok {
			if t.Variable != "" {
				sc.setRoot(t.Variable, shared)
			}
			return nil
		}
	}

	value, err := e.applyTransform(ctx, t, sc)
	if err != nil {
		return err
	}
	// HAPI-compat: if every parameter resolved to nil (i.e. the variable
	// references were all unbound), the target produces no value and is
	// treated as a no-op. This keeps best-effort transforms running when
	// upstream source data is sparse.
	if value == nil && len(t.Parameter) > 0 && t.Transform != "create" {
		return nil
	}
	if t.Element != "" {
		if err := writeTargetElement(holder, t.Element, value, t.Variable == "", targetWriteMode(t.ListMode)); err != nil {
			return err
		}
	}
	if sharing {
		sc.setShared(t.ListRuleId, value)
	}
	if t.Variable != "" {
		// target.variable bindings outlive the rule iteration — later
		// rules in the same group must still see them. Write to the root
		// frame so subsequent rules (whose scope-fork chains back to the
		// same root) can resolve the variable.
		sc.setRoot(t.Variable, value)
	}
	return nil
}

func hasListMode(modes []string, m string) bool {
	for _, x := range modes {
		if x == m {
			return true
		}
	}
	return false
}

func targetWriteMode(modes []string) string {
	for _, m := range modes {
		switch m {
		case "collate", "first", "last", "single":
			return m
		}
	}
	return ""
}

// applyTransform computes the value a target writes by dispatching on the
// transform code. Returns the value and any error from parameter resolution
// or external delegation.
//
// Spec reference: https://hl7.org/fhir/R5/valueset-map-transform.html
func (e *Engine) applyTransform(ctx context.Context, t *structuremap.Target, sc *scope) (any, error) {
	switch t.Transform {
	case "", "copy":
		return resolveParameter(t.Parameter, sc)
	case "create":
		// FHIR spec lets the parameter be a type hint. The created object is
		// what later targets write into via target.context binding. When the
		// hint names a FHIR *resource* type, stamp resourceType so mid-bundle
		// resources are valid FHIR (datatypes like Reference must NOT get it).
		// (EPIC-hapi-mapping-parity S3/G3.)
		m := map[string]any{}
		if len(t.Parameter) > 0 {
			if v, err := resolveParameter(t.Parameter, sc); err == nil && v != nil {
				if name := stringify(v); isResourceType(name) {
					m["resourceType"] = name
				}
			}
		}
		return m, nil
	case "truncate":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil || len(args) < 2 {
			return nil, fmt.Errorf("truncate requires (value, length); got %d args", len(args))
		}
		s := stringify(args[0])
		n := int64ify(args[1])
		if int64(len(s)) <= n {
			return s, nil
		}
		return s[:n], nil
	case "escape":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		s := stringify(args[0])
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		return s, nil
	case "append":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		var sb strings.Builder
		for _, a := range args {
			sb.WriteString(stringify(a))
		}
		return sb.String(), nil
	case "uuid":
		return newUUIDv4(), nil
	case "cast":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil || len(args) < 2 {
			return nil, fmt.Errorf("cast requires (value, typeName); got %d args", len(args))
		}
		v, err := castTo(args[0], stringify(args[1]))
		if err != nil {
			return e.acceptNonConformant("cast", t.Parameter, sc, err)
		}
		return v, nil
	case "reference":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil || len(args) < 1 {
			return nil, fmt.Errorf("reference requires (resource); got %d args", len(args))
		}
		resource, ok := args[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference: argument must be a resource map; got %T", args[0])
		}
		rt, _ := resource["resourceType"].(string)
		id, _ := resource["id"].(string)
		if rt == "" || id == "" {
			return nil, fmt.Errorf("reference: source resource missing resourceType or id")
		}
		return map[string]any{"reference": rt + "/" + id}, nil
	case "c":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil || len(args) < 2 {
			return nil, fmt.Errorf("c requires (system, code [, display]); got %d args", len(args))
		}
		coding := map[string]any{
			"system": stringify(args[0]),
			"code":   stringify(args[1]),
		}
		if len(args) >= 3 {
			coding["display"] = stringify(args[2])
		}
		return coding, nil
	case "cc":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		if len(args) == 1 {
			return map[string]any{"text": stringify(args[0])}, nil
		}
		if len(args) < 2 {
			return nil, fmt.Errorf("cc requires (text) or (system, code [, display]); got %d args", len(args))
		}
		coding := map[string]any{
			"system": stringify(args[0]),
			"code":   stringify(args[1]),
		}
		if len(args) >= 3 {
			coding["display"] = stringify(args[2])
		}
		return map[string]any{"coding": []any{coding}}, nil
	case "qty":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		if len(args) == 1 {
			return parseQtyText(stringify(args[0]))
		}
		if len(args) < 2 {
			return nil, fmt.Errorf("qty requires (text) or (value, unit [, system, code]); got %d args", len(args))
		}
		valFloat, err := strconv.ParseFloat(stringify(args[0]), 64)
		if err != nil {
			return nil, fmt.Errorf("qty: value must be numeric; got %v", args[0])
		}
		q := map[string]any{
			"value": valFloat,
			"unit":  stringify(args[1]),
		}
		if len(args) >= 4 {
			q["system"] = stringify(args[2])
			q["code"] = stringify(args[3])
		}
		return q, nil
	case "id":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		if len(args) <= 1 {
			return firstParamValue(args), nil
		}
		out := map[string]any{
			"system": stringify(args[0]),
			"value":  stringify(args[1]),
		}
		if len(args) >= 3 {
			out["type"] = args[2]
		}
		return out, nil
	case "cp":
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		if len(args) <= 1 {
			return firstParamValue(args), nil
		}
		out := map[string]any{
			"system": stringify(args[0]),
			"value":  stringify(args[1]),
		}
		if len(args) >= 3 {
			out["use"] = stringify(args[2])
		}
		return out, nil
	case "translate":
		return e.applyTranslate(ctx, t.Parameter, sc)
	case "evaluate":
		// Two forms (FHIR Mapping Language):
		//   evaluate(context, fhirpathExpr) — explicit context.
		//   (fhirpathExpr)                  — inline shorthand; the parser
		//                                     lowers it to a 1-arg evaluate.
		// For the inline form there is no explicit context, so the expression
		// is evaluated against the variable scope (every bound name as a field
		// of the subject), letting bare names like `v` resolve — the same
		// scope-relative evaluation HAPI performs for inline expressions.
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		env := sc.envSnapshot()
		var expr string
		var subject any
		switch len(args) {
		case 1:
			expr, subject = stringify(args[0]), env
		case 2:
			expr, subject = stringify(args[1]), args[0]
		default:
			return nil, fmt.Errorf("evaluate requires (fhirpathExpr) or (context, fhirpathExpr); got %d args", len(args))
		}
		result, err := fhirpath.EvalIn(expr, subject, env)
		if err != nil {
			return nil, fmt.Errorf("evaluate: %w", err)
		}
		if len(result) == 1 {
			return result[0], nil
		}
		return result, nil
	case "toDateTime":
		// Parse a string into FHIR dateTime (RFC3339 UTC). With format,
		// translate Java SimpleDateFormat → Go layout.
		v, err := applyToDateTime(t.Parameter, sc, fhirDateTimeOut)
		if err != nil {
			return e.acceptNonConformant("toDateTime", t.Parameter, sc, err)
		}
		return v, nil
	case "toDate":
		v, err := applyToDateTime(t.Parameter, sc, fhirDateOut)
		if err != nil {
			return e.acceptNonConformant("toDate", t.Parameter, sc, err)
		}
		return v, nil
	case "toTime":
		v, err := applyToDateTime(t.Parameter, sc, fhirTimeOut)
		if err != nil {
			return e.acceptNonConformant("toTime", t.Parameter, sc, err)
		}
		return v, nil
	case "unixToDateTime":
		// Integer or numeric-string Unix epoch → ISO-8601 UTC dateTime.
		return applyUnixTo(t.Parameter, sc, fhirDateTimeOut)
	case "unixToDate":
		return applyUnixTo(t.Parameter, sc, fhirDateOut)
	case "unixToTime":
		return applyUnixTo(t.Parameter, sc, fhirTimeOut)
	case "dateOp":
		// Apply ISO-8601 duration to date; supports Y/M/D components and combinations.
		args, err := resolveAllParameters(t.Parameter, sc)
		if err != nil {
			return nil, err
		}
		if len(args) <= 1 {
			return firstParamValue(args), nil
		}
		// Two dialects share the `dateOp` name:
		//   arithmetic    — dateOp(date, op, duration); op is "+"/"-"
		//   parse/format  — dateOp(value, javaFormat [, outputType]); a source MSH
		//     map uses this to turn an HL7 datetime into a FHIR dateTime/instant.
		// Disambiguate on arg[1]: "+"/"-" is arithmetic; anything else is a Java
		// date pattern. (S7 — prod dateOp parity.)
		if op := stringify(args[1]); op == "+" || op == "-" {
			if len(args) < 3 {
				return nil, fmt.Errorf("dateOp arithmetic requires (date, op, duration); got %d args", len(args))
			}
			return applyDateOp(stringify(args[0]), op, stringify(args[2]))
		}
		layout, lerr := javaPatternToGoLayout(stringify(args[1]))
		if lerr != nil {
			return e.acceptNonConformant("dateOp", t.Parameter, sc, lerr)
		}
		parsed, perr := time.Parse(layout, stringify(args[0]))
		if perr != nil {
			return e.acceptNonConformant("dateOp", t.Parameter, sc, perr)
		}
		return formatFHIR(parsed, fhirDateTimeOut), nil
	case "pointer":
		return resolveParameter(t.Parameter, sc)
	default:
		return nil, fmt.Errorf("transform %q is not recognised by this engine", t.Transform)
	}
}

// applyTranslate handles the `translate` transform by delegating to the
// ConceptMap $translate engine. Returns a string (for output type=code) or
// a Coding map (for Coding/CodeableConcept).
//
// M6.11: before consulting the external translator, the executor checks
// any inline ConceptMap declarations attached to the running
// StructureMap (StructureMap.Contained, populated by the FML parser
// from `conceptmap "url" { ... }` blocks). If the requested map URL
// matches an inline declaration, the lookup short-circuits to a local
// walk — no external dispatch required. This is the path FHIR
// fragment-URL references (`'#mymap'`) take.
func (e *Engine) applyTranslate(ctx context.Context, params []structuremap.Parameter, sc *scope) (any, error) {
	args, err := resolveAllParameters(params, sc)
	if err != nil || len(args) < 2 {
		return nil, fmt.Errorf("translate requires (sourceCode, mapURL [, outputType]); got %d args", len(args))
	}
	sourceCode := stringify(args[0])
	mapURL := stringify(args[1])
	outputType := "code"
	if len(args) >= 3 {
		outputType = stringify(args[2])
	}

	resp, err := e.translateRequest(ctx, sc, mapURL, sourceCode)
	if err != nil {
		return nil, fmt.Errorf("translate(%q via %s): %w", sourceCode, mapURL, err)
	}
	if !resp.Result || len(resp.Matches) == 0 {
		// Strict mode fails loud: a resolved map with no mapping would otherwise
		// silently drop the value. Detail names only the map URL (never the source code—PHI-safe).
		if e.strictTransform {
			return nil, fmt.Errorf("%w: %s", ErrTranslateNoMatch, mapURL)
		}
		// A RESOLVED ConceptMap with no mapping for this code is a data-coverage
		// gap, not a config error — the reference pipeline DROPS the unmapped
		// coding and continues (it does not fail the whole message). Match that:
		// drop and continue, logging a coverage report at ERROR when conformance
		// logging is enabled (PHI-safe: a lab/analyte code is not PHI, no value
		// is logged). Only an UNRESOLVABLE map errors (above) — that's S5.
		if e.conformanceLog != nil {
			e.conformanceLog.Error("HL7V2_NONCONFORMANT_ACCEPTED",
				"reason", "unmapped-code", "transform", "translate",
				"source_code", sourceCode, "map", mapURL)
		}
		return nil, nil
	}
	m := resp.Matches[0]
	switch outputType {
	case "code":
		if m.Concept != nil {
			return m.Concept.Code, nil
		}
		return "", nil
	case "Coding":
		if m.Concept == nil {
			return nil, fmt.Errorf("translate(%q via %s): match has no concept (result=true but Concept is nil); outputType=%q requires a concrete Coding", sourceCode, mapURL, outputType)
		}
		out := map[string]any{
			"system": m.Concept.System,
			"code":   m.Concept.Code,
		}
		if m.Concept.Display != "" {
			out["display"] = m.Concept.Display
		}
		return out, nil
	case "CodeableConcept":
		if m.Concept == nil {
			return nil, fmt.Errorf("translate(%q via %s): match has no concept (result=true but Concept is nil); outputType=%q requires a concrete CodeableConcept", sourceCode, mapURL, outputType)
		}
		coding := map[string]any{
			"system": m.Concept.System,
			"code":   m.Concept.Code,
		}
		if m.Concept.Display != "" {
			coding["display"] = m.Concept.Display
		}
		return map[string]any{"coding": []any{coding}}, nil
	default:
		return nil, fmt.Errorf("translate: unknown output type %q (want code|Coding|CodeableConcept)", outputType)
	}
}

// acceptNonConformant implements S6 (approach A) for value-coercion transforms
// (toDate*/cast/dateOp). When conformance logging is enabled, a coercion failure
// is ACCEPTED (the value is kept uncoerced) so the transform proceeds, and a
// report is logged at ERROR (PHI-safe: only the failing transform + reason,
// never the raw value). When logging is off it stays fail-closed (returns the
// original error) — a coercion failure may signal a map-authoring bug (e.g. a
// bad format pattern), so it is not silently swallowed by default.
//
// (Unmapped translate codes are different — a resolved ConceptMap with no entry
// is a pure data-coverage gap, so applyTranslate drops those by default.)
func (e *Engine) acceptNonConformant(transform string, params []structuremap.Parameter, sc *scope, cause error) (any, error) {
	// Strict mode fails loud on any coercion failure, overriding conformance
	// logging. Detail names only the transform — never the cause, which can
	// embed the raw source value (e.g. `parsing time "..."`) and may be PHI.
	if e.strictTransform {
		return nil, fmt.Errorf("%w: %s", ErrNonConformantCoercion, transform)
	}
	if e.conformanceLog == nil {
		return nil, cause
	}
	e.conformanceLog.Error("HL7V2_NONCONFORMANT_ACCEPTED",
		"reason", "coercion-failed", "transform", transform)
	if len(params) > 0 {
		if v, err := resolveParameter(params[:1], sc); err == nil {
			return v, nil // keep the raw (uncoerced) value so the pipeline proceeds
		}
	}
	return nil, nil
}

// translateRequest dispatches a (mapURL, sourceCode) lookup. M6.11: when
// the URL matches an inline ConceptMap declared via FML's `conceptmap`
// block (StructureMap.Contained), the lookup walks that map's groups
// locally. Otherwise it falls through to the external translator.
//
// Returns a *translate.Response shaped the same way the external engine
// would — so the caller's output-type switch handles both paths
// uniformly.
func (e *Engine) translateRequest(ctx context.Context, sc *scope, mapURL, sourceCode string) (*translate.Response, error) {
	if cm := lookupInlineConceptMap(sc, mapURL); cm != nil {
		return translateInline(cm, sourceCode), nil
	}
	if e.translator == nil {
		return nil, fmt.Errorf("translate transform requires a ConceptMap engine; none was wired and no inline map matched %q", mapURL)
	}
	return e.translator.Translate(ctx, translate.Request{
		URL:        mapURL,
		SourceCode: sourceCode,
	})
}

// lookupInlineConceptMap walks the executing StructureMap's Contained
// list for a ConceptMap whose URL matches `url`. A fragment reference
// (`"#name"`) also matches a contained map whose ID equals `name` —
// that's how FHIR-style contained-resource references resolve.
func lookupInlineConceptMap(sc *scope, url string) *conceptmap.ConceptMap {
	root := sc.root()
	if root.sm == nil || len(root.sm.Contained) == 0 {
		return nil
	}
	fragmentID := ""
	if strings.HasPrefix(url, "#") {
		fragmentID = url[1:]
	}
	for _, cm := range root.sm.Contained {
		if cm == nil {
			continue
		}
		if cm.URL == url {
			return cm
		}
		// Match a `#fragment` reference against the contained resource's id.
		// Contained ids are sometimes stored with the leading '#' included, so
		// normalise both sides before comparing.
		if fragmentID != "" && strings.TrimPrefix(cm.ID, "#") == fragmentID {
			return cm
		}
	}
	return nil
}

// translateInline walks a single ConceptMap looking for the source code
// across all groups, mirroring the small subset of the translate engine
// the inline path needs. Targets whose relationship is `not-related-to`
// are still returned — the caller distinguishes via Response.Result.
func translateInline(cm *conceptmap.ConceptMap, sourceCode string) *translate.Response {
	resp := &translate.Response{}
	for _, g := range cm.Group {
		for _, el := range g.Element {
			if el.Code != sourceCode {
				continue
			}
			for ti := range el.Target {
				tgt := &el.Target[ti]
				resp.Matches = append(resp.Matches, fhir.TranslateMatch{
					Relationship: tgt.Relationship,
					Concept: &fhir.Coding{
						System:  g.Target,
						Code:    tgt.Code,
						Display: tgt.Display,
					},
					OriginMap: cm.URL,
				})
				if tgt.Relationship != "not-related-to" {
					resp.Result = true
				}
			}
		}
	}
	if !resp.Result && len(resp.Matches) == 0 {
		resp.Message = "No mapping found for the provided concept"
	}
	return resp
}

func resolveAllParameters(params []structuremap.Parameter, sc *scope) ([]any, error) {
	out := make([]any, 0, len(params))
	for i := range params {
		v, err := resolveParameter(params[i:i+1], sc)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

func int64ify(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func castTo(v any, typeName string) (any, error) {
	switch typeName {
	case "string", "code":
		return stringify(v), nil
	case "integer":
		switch x := v.(type) {
		case int64:
			return x, nil
		case int:
			return int64(x), nil
		case float64:
			return int64(x), nil
		case string:
			n, err := strconv.ParseInt(x, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("cast: %q is not an integer", x)
			}
			return n, nil
		}
		return nil, fmt.Errorf("cast: cannot cast %T to integer", v)
	case "decimal":
		switch x := v.(type) {
		case float64:
			return x, nil
		case int64:
			return float64(x), nil
		case string:
			n, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return nil, fmt.Errorf("cast: %q is not a decimal", x)
			}
			return n, nil
		}
		return nil, fmt.Errorf("cast: cannot cast %T to decimal", v)
	case "boolean":
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			return x == "true", nil
		}
		return nil, fmt.Errorf("cast: cannot cast %T to boolean", v)
	}
	return nil, fmt.Errorf("cast: unsupported target type %q", typeName)
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b[:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:])
}

var _ = fhir.Coding{}

// resolveParameter resolves a Parameter slice to a single Go value. The
// usual case is one parameter — a variable reference via ValueID. Literal
// value[x] fields fall through to their typed slots.
func resolveParameter(params []structuremap.Parameter, sc *scope) (any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	p := params[0]
	switch {
	case p.ValueID != "":
		// HAPI-compat: when a parameter references a variable that no
		// earlier rule managed to bind (typically because the rule's
		// source didn't match anything), treat it as "no value" — the
		// target write becomes a no-op rather than a hard transform
		// failure. This matches HAPI's permissive behaviour and lets a
		// best-effort transform produce a partial target instead of
		// erroring out the whole request.
		v, ok := sc.get(p.ValueID)
		if !ok {
			return nil, nil
		}
		return v, nil
	case p.ValueString != "":
		return p.ValueString, nil
	case p.ValueInteger != nil:
		return *p.ValueInteger, nil
	case p.ValueBoolean != nil:
		return *p.ValueBoolean, nil
	case p.ValueDecimal != nil:
		return *p.ValueDecimal, nil
	case p.ValueDate != "":
		return p.ValueDate, nil
	case p.ValueDateTime != "":
		return p.ValueDateTime, nil
	case p.ValueTime != "":
		return p.ValueTime, nil
	}
	return nil, nil
}

func truthyFP(c []any) bool {
	if len(c) == 0 {
		return false
	}
	if len(c) == 1 {
		if b, ok := c[0].(bool); ok {
			return b
		}
	}
	return true
}

// scopeFork creates a child scope that inherits parent bindings via the
// parent pointer chain. get() walks up to the parent; setRoot() walks to
// the original root so target.variable bindings remain visible after the fork
// pops—matching the contract that those bindings outlive rule iterations.
func scopeFork(parent *scope) *scope {
	child := newScope()
	child.parent = parent
	return child
}

var _ context.Context // keep ctx parameter useful for operations that consult $translate over the network or DB
func firstParamValue(args []any) any {
	if len(args) == 0 {
		return nil
	}
	return args[0]
}

type fhirOutKind int

const (
	fhirDateTimeOut fhirOutKind = iota
	fhirDateOut
	fhirTimeOut
)

func formatFHIR(t time.Time, kind fhirOutKind) string {
	switch kind {
	case fhirDateOut:
		return t.Format("2006-01-02")
	case fhirTimeOut:
		return t.Format("15:04:05")
	default:
		return t.UTC().Format("2006-01-02T15:04:05Z")
	}
}

// applyToDateTime implements toDateTime/toDate/toTime parsing per
// FHIR R5 mapping spec. With one arg, the value must already be a
// recognisable ISO form; with two args, the second is a Java
// SimpleDateFormat pattern that we translate into Go's reference
// layout.
func applyToDateTime(params []structuremap.Parameter, sc *scope, kind fhirOutKind) (any, error) {
	args, err := resolveAllParameters(params, sc)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("toDate/toDateTime/toTime requires (value [, format])")
	}
	input := stringify(args[0])
	if len(args) >= 2 {
		pattern := stringify(args[1])
		layout, err := javaPatternToGoLayout(pattern)
		if err != nil {
			return nil, err
		}
		parsed, err := time.Parse(layout, input)
		if err != nil {
			return nil, fmt.Errorf("toDate*: cannot parse %q with format %q: %w", input, pattern, err)
		}
		return formatFHIR(parsed, kind), nil
	}
	// No format hint — try common ISO shapes in priority order.
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
		"15:04:05",
	} {
		if parsed, err := time.Parse(layout, input); err == nil {
			return formatFHIR(parsed, kind), nil
		}
	}
	return nil, fmt.Errorf("toDate*: could not parse %q as ISO; supply an explicit format argument", input)
}

// applyUnixTo turns a Unix epoch (integer or numeric string) into a
// FHIR-formatted dateTime/date/time string in UTC.
func applyUnixTo(params []structuremap.Parameter, sc *scope, kind fhirOutKind) (any, error) {
	args, err := resolveAllParameters(params, sc)
	if err != nil {
		return nil, err
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("unixTo*: requires (epoch-seconds)")
	}
	var seconds int64
	switch v := args[0].(type) {
	case int:
		seconds = int64(v)
	case int32:
		seconds = int64(v)
	case int64:
		seconds = v
	case float64:
		seconds = int64(v)
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unixTo*: cannot parse %q as integer epoch: %w", v, err)
		}
		seconds = n
	default:
		return nil, fmt.Errorf("unixTo*: epoch arg must be integer or numeric string; got %T", v)
	}
	return formatFHIR(time.Unix(seconds, 0).UTC(), kind), nil
}

// javaDateTokens maps Java SimpleDateFormat letter-runs to Go
// reference-layout fragments. A letter-run absent from this table is an
// unsupported token and fails loud — translating into a layout that
// only matches a literal character would silently mismatch real data
// (e.g. a literal 'Z' matches UTC suffixes but rejects "+0200").
var javaDateTokens = map[string]string{
	"yyyy": "2006",
	"yy":   "06",
	"MM":   "01",
	"M":    "1",
	"dd":   "02",
	"d":    "2",
	"HH":   "15", // 24-hour; Go has no unpadded 24-hour token, so "H" stays unsupported
	"hh":   "03", // 12-hour, pairs with "a"
	"h":    "3",
	"mm":   "04",
	"m":    "4",
	"ss":   "05",
	"s":    "5",
	"SSS":  "000",   // only valid after '.' or ',' — see the tokenizer guard
	"a":    "PM",    // AM/PM marker
	"Z":    "Z0700", // zone: matches literal 'Z' (UTC) or a numeric offset like +0200
	"T":    "T",     // ISO date/time separator, kept literal
}

// javaPatternToGoLayout translates a small subset of Java
// SimpleDateFormat patterns (the ones we actually see in hospital ETL)
// into Go's reference time layout. Unsupported patterns return an
// error rather than silently mismatching.
func javaPatternToGoLayout(pattern string) (string, error) {
	isLetter := func(c byte) bool {
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	var b strings.Builder
	for i := 0; i < len(pattern); {
		c := pattern[i]
		if !isLetter(c) {
			b.WriteByte(c)
			i++
			continue
		}
		j := i + 1
		for j < len(pattern) && pattern[j] == c {
			j++
		}
		run := pattern[i:j]
		if run == "SSS" {
			// Go's only fractional-second token is ".000"/",000" — the
			// separator is part of the token. A bare "SSS" would emit three
			// LITERAL zeros that only match millisecond-000 values: the
			// silent-mismatch class this translator exists to prevent.
			if prev := b.String(); prev == "" || (prev[len(prev)-1] != '.' && prev[len(prev)-1] != ',') {
				return "", fmt.Errorf("unsupported date-format token %q in pattern %q (Go fractional seconds need the separator: use \".SSS\")", run, pattern)
			}
		}
		layout, ok := javaDateTokens[run]
		if !ok {
			return "", fmt.Errorf("unsupported date-format token %q in pattern %q", run, pattern)
		}
		b.WriteString(layout)
		i = j
	}
	return b.String(), nil
}

// applyDateOp implements the dateOp(date, op, duration) transform per
// the FHIR R5 mapping spec. op is "+" or "-"; duration is an ISO-8601
// duration with Y / M / D components. Date input is YYYY-MM-DD (or the
// longer date-time forms, in which case we preserve everything after
// the date).
func applyDateOp(dateStr, op, duration string) (string, error) {
	// Split off any time portion so we operate on the date head only.
	dateHead, tail := dateStr, ""
	if idx := strings.IndexAny(dateStr, "T "); idx >= 0 {
		dateHead = dateStr[:idx]
		tail = dateStr[idx:]
	}
	t, err := time.Parse("2006-01-02", dateHead)
	if err != nil {
		return "", fmt.Errorf("dateOp: cannot parse date %q: %w", dateStr, err)
	}
	years, months, days, err := parseISODuration(duration)
	if err != nil {
		return "", err
	}
	switch op {
	case "+":
		t = t.AddDate(years, months, days)
	case "-":
		t = t.AddDate(-years, -months, -days)
	default:
		return "", fmt.Errorf("dateOp: op must be '+' or '-'; got %q", op)
	}
	return t.Format("2006-01-02") + tail, nil
}

// parseQtyText parses the FHIR-mapping `qty(text)` one-arg form into a
// Quantity map. Recognised shapes:
//
//	"5"          → {value: 5}
//	"5 mg"       → {value: 5, unit: "mg"}                        — free unit
//	"5 'mg'"     → {value: 5, unit: "mg", code: "mg",
//	                system: "http://unitsofmeasure.org"}         — UCUM-coded
//
// Anything that doesn't begin with a parseable numeric prefix is rejected
// loudly — a silent default would mask authoring bugs in callers that
// expect numeric inputs.
func parseQtyText(text string) (any, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil, fmt.Errorf("qty: text is empty")
	}
	// Split into value + remainder on the first whitespace run.
	cut := len(s)
	for i, r := range s {
		if r == ' ' || r == '\t' {
			cut = i
			break
		}
	}
	valStr := s[:cut]
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return nil, fmt.Errorf("qty: %q has no parseable numeric prefix", text)
	}
	q := map[string]any{"value": val}
	rest := strings.TrimSpace(s[cut:])
	if rest == "" {
		return q, nil
	}
	// Quoted unit ⇒ UCUM-coded Quantity.
	if len(rest) >= 2 && rest[0] == '\'' && rest[len(rest)-1] == '\'' {
		code := rest[1 : len(rest)-1]
		q["system"] = "http://unitsofmeasure.org"
		q["code"] = code
		q["unit"] = code
		return q, nil
	}
	q["unit"] = rest
	return q, nil
}

// parseISODuration parses a small subset of ISO-8601 duration syntax:
// P<n>Y, P<n>M, P<n>D, and combinations like P1Y2M3D. Time components
// (PT...H/M/S) are rejected — that's the future work tier for dateOp.
func parseISODuration(s string) (years, months, days int, err error) {
	if len(s) < 2 || s[0] != 'P' {
		return 0, 0, 0, fmt.Errorf("ISO-8601 duration must start with 'P'; got %q", s)
	}
	if strings.Contains(s, "T") {
		return 0, 0, 0, fmt.Errorf("dateOp: time components (PT…H/M/S) are not yet supported in %q", s)
	}
	i := 1
	for i < len(s) {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i || j == len(s) {
			return 0, 0, 0, fmt.Errorf("malformed ISO-8601 duration %q", s)
		}
		n, _ := strconv.Atoi(s[i:j])
		switch s[j] {
		case 'Y':
			years = n
		case 'M':
			months = n
		case 'D':
			days = n
		default:
			return 0, 0, 0, fmt.Errorf("unsupported ISO-8601 duration unit %q in %q", string(s[j]), s)
		}
		i = j + 1
	}
	return years, months, days, nil
}
