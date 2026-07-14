// Package transform implements the FHIR StructureMap $transform operation.
//
// The architecture splits into:
//
//	internal/transform/fhirpath   — FHIRPath subset for `source.element` paths
//	                                and `source.condition` / `source.check`
//	                                predicates
//	internal/transform/fml        — FHIR Mapping Language lexer + parser
//	                                (M5b — accepts FML text as an alternative
//	                                input to the StructureMap JSON form)
//	internal/transform/...         — this package, the rule-walking executor
//
// The executor consumes a parsed StructureMap (the same shape persisted by
// the M5a CRUD path) and a JSON-shaped source value, and returns the
// JSON-shaped target value.
//
// Transform vocabulary lands in phases:
//
//	M5d — `copy`, `create`
//	M5e — nested groups + `dependent` rules
//	M5f — `translate`, `evaluate`, `append`, `cast`, `c`, `cc`, `cp`, `qty`,
//	      `id`, `escape`, `reference`, `dateOp`, `uuid`, `pointer`, `truncate`
package transform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/transform/fhirpath"
	"github.com/goodeworkers/fhir-map/internal/transform/hl7v2"
	"github.com/goodeworkers/fhir-map/internal/transform/resolver"
	"github.com/goodeworkers/fhir-map/internal/translate"
)

// Sentinel errors that handlers map to FHIR HTTP status/code pairs.
//
// Callers use errors.Is to classify engine errors; the wrapped detail
// is extracted via strings.TrimPrefix(err.Error(), Sentinel.Error()+": ")
// so the handler can surface contractually-required names (type names,
// group references) without echoing the sentinel prose. [SYM-GR-0013]
var (
	// ErrInputInvalid is returned when the source resource is structurally
	// empty (no required fields). Maps to HTTP 422, FHIR code "invalid".
	// [SYM-GR-0013] The handler MUST use a generic diagnostic — do not
	// echo err.Error() into the HTTP response.
	ErrInputInvalid = errors.New("input resource missing required fields")

	// ErrInputTypeMismatch is returned when the source resourceType does not
	// match the type declared on the StructureMap's first source-mode input.
	// Maps to HTTP 422, FHIR code "invalid". The wrapped detail names both
	// types (and may include "(via extends %q)" when the mismatch is detected
	// through an extends parent); it is not PHI.
	ErrInputTypeMismatch = errors.New("input type does not match StructureMap declared input")

	// ErrMapNotFound is returned when a group reference (dependent / extends)
	// or a map URL reference cannot be resolved. Maps to HTTP 422, FHIR
	// code "not-found". The wrapped detail names the reference and IS the
	// public contract (AC-6 requires it); it is not PHI.
	ErrMapNotFound = errors.New("referenced map or group not loaded")

	// ErrRecursionLimit is returned when a dependent-group call chain exceeds
	// MaxGroupRecursionDepth. Maps to HTTP 422, FHIR code "too-costly".
	// The wrapped detail names the group reference that triggered the cap.
	ErrRecursionLimit = errors.New("group recursion depth limit exceeded")

	// ErrCheckFailed is returned when a source.check FHIRPath assertion
	// evaluates to false. Maps to HTTP 422, FHIR code "invariant". The
	// wrapped detail carries the verbatim check expression (part of the
	// StructureMap's public contract; not PHI).
	ErrCheckFailed = errors.New("source.check assertion failed")

	// ErrTransformCanceled is returned when the execution context is canceled
	// or its deadline expires mid-transform. Maps to HTTP 422, FHIR code
	// "too-costly". It wraps the underlying context error (errors.Is against
	// context.DeadlineExceeded / context.Canceled still holds). This is the
	// teeth behind the per-request transform time budget: the recursion cap
	// bounds dependent-call DEPTH, but the cartesian source-product can fan
	// out by data BREADTH within the cap, so the engine cooperatively checks
	// for cancellation at each group and source-product node.
	ErrTransformCanceled = errors.New("transform canceled or timed out")

	// ErrTargetListSingle is returned when a target with listMode `single`
	// would be written more than once (the source produced more than one
	// item). Maps to HTTP 422, FHIR code "invariant". The wrapped detail
	// names the target element (part of the map's public contract; not PHI).
	ErrTargetListSingle = errors.New("target.listMode = single but written more than once")

	// ErrNonConformantCoercion is returned in strict transform mode when a
	// typed transform (cast / toDate / toDateTime / toTime / dateOp) cannot
	// coerce its input. Maps to HTTP 422, FHIR code "value". The wrapped detail
	// names the transform only (never the source value — PHI-conservative).
	ErrNonConformantCoercion = errors.New("strict: typed transform could not coerce a non-conformant value")

	// ErrTranslateNoMatch is returned in strict transform mode when a resolved
	// ConceptMap has no mapping for the source code (a translate that would
	// otherwise silently drop the value — the canonical ETL data-loss case).
	// Maps to HTTP 422, FHIR code "not-found". The wrapped detail names the map
	// URL only (never the source code — PHI-conservative).
	ErrTranslateNoMatch = errors.New("strict: code not mapped by the resolved ConceptMap")
)

// checkContext reports a wrapped ErrTransformCanceled if ctx is done. Called
// at the top of the engine's data-driven loop nodes (runGroup,
// iterateSourceProduct) so a context deadline interrupts a pathological
// transform within one iteration — Go contexts are cooperative and a tight
// CPU loop will not otherwise observe the deadline.
func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrTransformCanceled, err)
	}
	return nil
}

// MaxGroupRecursionDepth caps the depth of dependent→dependent group chains.
// 5 is the spec'd ceiling: AC-4 requires "5 or more nested invocations"
// to trip ErrRecursionLimit. Compared at the top of runDependent with `>=`,
// post-increment, so the 5th invocation is the one that fails.
//
// Cross-feature note: ConceptMap $translate enforces an analogous cap
// `otherMapMaxDepth` (see `internal/translate/engine.go`) on chained
// `unmapped.mode = other-map` lookups. The two caps are independent — one
// guards the transform engine's dependent recursion, the other guards the
// translate engine's fallback recursion — but should stay numerically
// aligned unless a deliberate divergence is documented.
const MaxGroupRecursionDepth = 5

// MapResolver resolves a StructureMap canonical URL to an AST. Used to
// honour FML `imports` declarations at execution time without coupling the
// transform package to the storage layer. The transform package defines
// the interface; *structuremap.Service satisfies it structurally.
type MapResolver interface {
	FindByURL(ctx context.Context, url string, version string) (*structuremap.StructureMap, error)
}

// Engine runs the $transform operation.
//
// The optional `translator` is used by the `translate` transform code (M5f)
// — when nil, that transform returns an error. Other transforms work
// without it.
//
// The optional `mapResolver` is used to resolve FML `imports` declarations
// at execution time. When nil, imports raise ErrMapNotFound.
//
// The optional `typeResolver` resolves canonical/profile URLs in group input
// Type fields to short FHIR type names. When nil, exact-string compare is
// used (back-compat; short-name Type declarations continue to work).
type Engine struct {
	translator   translate.Translator
	mapResolver  MapResolver
	typeResolver resolver.Resolver
	// conformanceLog, when set, enables S6 (approach A): typed transforms that
	// accept non-conformant input rather than failing emit an ERROR-level,
	// PHI-safe `HL7V2_NONCONFORMANT_ACCEPTED` log instead of silently degrading.
	// nil = disabled (default), preserving fail-closed behaviour.
	conformanceLog *slog.Logger
	// strictTransform, when true (opt-in, default false), makes the engine
	// FAIL LOUD on the silent-data-loss cases an ETL pipeline must not tolerate
	// — currently a typed-transform coercion failure and a resolved ConceptMap
	// with no mapping for the code — instead of best-effort accepting/dropping.
	// It OVERRIDES conformanceLog: a strict engine errors even when conformance
	// logging is on. Default false is byte-identical to today's lenient engine.
	strictTransform bool
}

// Option configures an Engine.
type Option func(*Engine)

// WithTranslator sets the translate.Translator for `translate(...)` transforms.
func WithTranslator(t translate.Translator) Option {
	return func(e *Engine) { e.translator = t }
}

// WithMapResolver sets the MapResolver for FML `imports` resolution.
func WithMapResolver(r MapResolver) Option {
	return func(e *Engine) { e.mapResolver = r }
}

// WithTypeResolver sets the Resolver used to map canonical/profile URLs in
// group input Type to short FHIR type names (closes CR-2.2-D14 / Story 2.4).
func WithTypeResolver(r resolver.Resolver) Option {
	return func(e *Engine) { e.typeResolver = r }
}

// WithConformanceLogging enables S6 (approach A): when a typed transform accepts
// non-conformant input instead of failing (e.g. a resolved ConceptMap with no
// mapping for the code), the engine logs it at ERROR (PHI-safe) and continues.
// Without it, those cases keep the existing fail-closed behaviour.
func WithConformanceLogging(l *slog.Logger) Option {
	return func(e *Engine) { e.conformanceLog = l }
}

// WithStrictTransform enables fail-loud strict mode (opt-in; default lenient).
// In strict mode the engine returns a precise error (wrapped in a strict
// sentinel) instead of silently accepting a coercion failure or dropping an
// unmapped translate code, so an embedding ETL pipeline can quarantine rather
// than ship structurally-incomplete data. Strict overrides conformance logging.
func WithStrictTransform(strict bool) Option {
	return func(e *Engine) { e.strictTransform = strict }
}

// New creates an Engine configured by the provided options.
func New(opts ...Option) *Engine {
	e := &Engine{}
	for _, o := range opts {
		o(e)
	}
	return e
}

// NewEngine wires a transform Engine. Pass a non-nil translator if the
// StructureMaps you serve include `translate(map-url, ...)` transforms.
// Preserved for back-compat; new callers should prefer New(...).
func NewEngine(translator translate.Translator) *Engine {
	return New(WithTranslator(translator))
}

// Transform runs the StructureMap against a source value, returning the
// target. The source must be a JSON-shaped value (a map, a slice, or a
// scalar) — typed Go structs are not yet supported; callers should
// marshal/unmarshal through `encoding/json` at the boundary.
func (e *Engine) Transform(ctx context.Context, sm *structuremap.StructureMap, source any) (any, error) {
	if sm == nil {
		return nil, fmt.Errorf("StructureMap is required")
	}
	if len(sm.Group) == 0 {
		return nil, fmt.Errorf("StructureMap has no group to execute")
	}

	// HAPI-compat: when the source is a Binary resource carrying HL7v2 or
	// HPRIM text, adapt it into a navigable map so segment-field paths
	// like `H-2`, `MSH-9`, `PID-3` resolve. Without this the FHIRPath
	// evaluator sees `Binary.data` (the base64 blob) instead of the
	// parsed segments.
	if hl7v2.IsHL7v2Binary(source) {
		adapted, err := hl7v2.AdaptBinary(source)
		if err != nil {
			return nil, fmt.Errorf("HL7v2 source adapter: %w", err)
		}
		source = adapted
	}

	entry := entryGroup(sm)
	if entry == nil {
		return nil, fmt.Errorf("StructureMap has no resolvable entry group")
	}

	// Pre-resolve FML imports BEFORE the AC-7 extends-type check so the
	// extends parent lookup can walk imported maps too (AC-7 must apply
	// even when the parent group lives in an imported map). Transitive
	// imports are followed via BFS with cycle detection.
	imported, err := e.resolveImports(ctx, sm)
	if err != nil {
		return nil, err
	}

	// AC-7 — extends parent-type mismatch must fire BEFORE the runtime
	// leaf-vs-source-resource type check (AC-4) so a static type-spec bug
	// is reported even when the runtime resource also happens to mismatch.
	// Walks imported maps so cross-map extends declarations are type-checked.
	if entry.Extends != "" {
		if parent := findGroupAcrossMaps(sm, imported, entry.Extends); parent != nil {
			leafType, _ := entrySourceInputType(entry)
			parentType, _ := entrySourceInputType(parent)
			if leafType != "" && parentType != "" && leafType != parentType {
				return nil, fmt.Errorf("%w: leaf group %q declares %q but parent group %q declares %q",
					ErrInputTypeMismatch, entry.Name, leafType, parent.Name, parentType)
			}
		}
	}

	// AC-4 / Story 2.4 — input type resolution: when srcType is a canonical
	// or profile URL, the typeResolver walks baseDefinition to a short name
	// before comparing against the runtime resourceType. Closes CR-2.2-D14.
	// See internal/transform/resolver/ for the resolution algorithm.
	if srcType, ok := entrySourceInputType(entry); ok && srcType != "" {
		if srcMap, isMap := source.(map[string]any); isMap {
			if gotType, _ := srcMap["resourceType"].(string); gotType != "" {
				resolvedType := srcType
				// Alias expansion: FML `uses "url" alias X as source` binds X to
				// the URL; group inputs that reference the alias (no "://") must
				// be expanded to the URL before URL resolution runs.
				if !strings.Contains(resolvedType, "://") {
					for _, s := range sm.Structure {
						if s.Alias == resolvedType && s.URL != "" {
							resolvedType = s.URL
							break
						}
					}
				}
				if e.typeResolver != nil && strings.Contains(resolvedType, "://") {
					rt, err := e.typeResolver.ResolveType(ctx, resolvedType)
					if err != nil {
						// [SYM-GR-0013] AC-3/AC-5: URL + remediation are the public
						// contract; not PHI. Wrap in ErrMapNotFound so the existing
						// handler error mapping emits 422 code: not-found.
						return nil, fmt.Errorf("%w: %s. Referenced StructureDefinition is not loaded. POST the StructureDefinition to /fhir/R5/StructureDefinition to register it",
							ErrMapNotFound, resolvedType)
					}
					resolvedType = rt
				}
				if resolvedType != gotType {
					// Named types are part of the public AC-4 contract; not PHI.
					return nil, fmt.Errorf("%w: expected %q, got %q", ErrInputTypeMismatch, resolvedType, gotType)
				}
			}
		}
	}

	// AC-3 — required-field tripwire: an input object with no keys other
	// than "resourceType" is clearly not a usable resource. This is
	// intentionally narrow — full FHIR profile validation is out of scope.
	if srcMap, isMap := source.(map[string]any); isMap {
		if isEffectivelyEmpty(srcMap) {
			return nil, fmt.Errorf("%w: input resource has no fields", ErrInputInvalid)
		}
	}

	// S4 — fold FML imports into the entry map via merge-by-name so imported
	// groups' rules and contained ConceptMaps compose into one map, run once
	// (matches the reference HAPI mapping engine). No-op when there are no imports.
	merged := mergeImports(sm, imported)
	entry = entryGroup(merged)
	if entry == nil {
		return nil, fmt.Errorf("StructureMap has no resolvable entry group after import merge")
	}

	target := map[string]any{}
	sc := newScope()
	sc.setMap(merged) // merged map exposes all groups + contained for dependent/translate lookups
	if len(imported) > 0 {
		sc.setImports(imported)
	}

	// Inject top-level `let` constants (FML map-scoped bindings).
	for i := range sm.Const {
		c := &sm.Const[i]
		if len(c.Source) != 1 {
			continue
		}
		s := &c.Source[0]
		vals, err := fhirpath.EvalIn(s.Element, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("top-level let %q: %w", s.Variable, err)
		}
		if len(vals) == 1 {
			sc.set(s.Variable, vals[0])
		} else if len(vals) > 1 {
			sc.set(s.Variable, vals)
		}
	}

	// Entry group starts at depth 0; extends runs parent rules inline
	// (no depth increment — only dependent→dependent chains count).
	if err := e.runGroup(ctx, entry, source, target, sc, 0); err != nil {
		return nil, err
	}
	// Resolve FHIR polymorphic value[x] elements (e.g. `value` -> `valueQuantity`)
	// on any resources in the output, inferring the type from the value's shape.
	normalizeChoiceTypes(target)
	return target, nil
}

// findGroupAcrossMaps searches sm.Group then each imported map's Group for a
// group with the given name. Used by Transform for the static extends
// parent-type check (AC-7) before scope is wired. Mirrors the lookup
// behaviour of scope.lookupGroupAcrossImports so the static and runtime
// resolutions stay consistent.
func findGroupAcrossMaps(sm *structuremap.StructureMap, imported []*structuremap.StructureMap, name string) *structuremap.Group {
	if sm != nil {
		for i := range sm.Group {
			if sm.Group[i].Name == name {
				return &sm.Group[i]
			}
		}
	}
	for _, imp := range imported {
		if imp == nil {
			continue
		}
		for i := range imp.Group {
			if imp.Group[i].Name == name {
				return &imp.Group[i]
			}
		}
	}
	return nil
}

// resolveImports walks sm.Import via the engine's mapResolver, then follows
// the transitive imports of each loaded map (BFS) with cycle detection so
// import graphs that loop or share branches are safe.
//
// Returns nil (no error) when no resolver is wired or sm has no imports —
// callers may still surface ErrMapNotFound at the dependent-lookup site for
// references that needed resolution but found nothing.
//
// errors.Is(err, structuremap.ErrNotFound) is collapsed to ErrMapNotFound
// with the offending URL in the wrapped detail; other errors (DB outage,
// context cancellation) propagate with their cause preserved so the
// handler default-branch can log them as exception.
func (e *Engine) resolveImports(ctx context.Context, sm *structuremap.StructureMap) ([]*structuremap.StructureMap, error) {
	if e.mapResolver == nil || sm == nil || len(sm.Import) == 0 {
		return nil, nil
	}
	visited := map[string]bool{}
	if sm.URL != "" {
		visited[sm.URL] = true
	}
	var out []*structuremap.StructureMap
	queue := append([]string(nil), sm.Import...)
	for len(queue) > 0 {
		url := queue[0]
		queue = queue[1:]
		if url == "" || visited[url] {
			continue
		}
		visited[url] = true
		imp, err := e.mapResolver.FindByURL(ctx, url, "")
		if err != nil {
			if errors.Is(err, structuremap.ErrNotFound) {
				return nil, fmt.Errorf("%w: import %q", ErrMapNotFound, url)
			}
			return nil, fmt.Errorf("resolve import %q: %w", url, err)
		}
		// Defensive nil check: a buggy MapResolver might return (nil, nil)
		// instead of (nil, ErrNotFound). Treat that as a not-found rather
		// than dereferencing the nil pointer downstream.
		if imp == nil {
			return nil, fmt.Errorf("%w: import %q (resolver returned nil)", ErrMapNotFound, url)
		}
		out = append(out, imp)
		queue = append(queue, imp.Import...)
	}
	return out, nil
}

// entryGroup returns the group to start $transform from. When extends
// inheritance is present the "leaf" group (one not extended by any other
// group) is the intended entry point; otherwise Group[0] is used. Returns
// nil when sm has zero groups (the caller MUST nil-check; Transform guards
// this above via len(sm.Group) == 0).
func entryGroup(sm *structuremap.StructureMap) *structuremap.Group {
	if sm == nil || len(sm.Group) == 0 {
		return nil
	}
	extended := make(map[string]bool, len(sm.Group))
	for i := range sm.Group {
		if sm.Group[i].Extends != "" {
			extended[sm.Group[i].Extends] = true
		}
	}
	for i := range sm.Group {
		if !extended[sm.Group[i].Name] {
			return &sm.Group[i]
		}
	}
	return &sm.Group[0]
}

// runGroup walks every rule in a group and applies it.
//
// The group's `input` block declares which scope variables map to the
// source and target roots. M5d-vintage groups have at most one source
// input and one target input; M5e generalises this.
//
// depth is the current dependent-call depth; extends rules run inline
// and do NOT increment it (only dependent→dependent chains count toward
// MaxGroupRecursionDepth).
func (e *Engine) runGroup(ctx context.Context, group *structuremap.Group, source, target any, sc *scope, depth int) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	sourceVar, targetVar := groupInputVars(group)
	if sourceVar != "" {
		sc.set(sourceVar, source)
	}
	if targetVar != "" {
		sc.set(targetVar, target)
	}
	// AC-8: if the declared parent group cannot be found, fail loud rather than
	// silently skipping it — the previous silent-no-op hid invalid StructureMaps.
	// Parent rules run first, extending rules run inline (no depth increment).
	if group.Extends != "" {
		parent := sc.lookupGroupAcrossImports(group.Extends)
		if parent == nil {
			return fmt.Errorf("%w: parent group %q", ErrMapNotFound, group.Extends)
		}
		for i := range parent.Rule {
			if err := e.runRule(ctx, &parent.Rule[i], sc, depth); err != nil {
				return fmt.Errorf("rule %q (inherited from %s): %w", parent.Rule[i].Name, group.Extends, err)
			}
		}
	}
	for i := range group.Rule {
		if err := e.runRule(ctx, &group.Rule[i], sc, depth); err != nil {
			return fmt.Errorf("rule %q: %w", group.Rule[i].Name, err)
		}
	}
	return nil
}

// groupInputVars returns the variable names a group binds for its source
// and target inputs. The first `mode=source` input wins for the source
// slot; the first `mode=target` for the target.
func groupInputVars(g *structuremap.Group) (srcVar, tgtVar string) {
	var src, tgt string
	for _, in := range g.Input {
		switch in.Mode {
		case "source":
			if src == "" {
				src = in.Name
			}
		case "target":
			if tgt == "" {
				tgt = in.Name
			}
		}
	}
	return src, tgt
}

// entrySourceInputType returns the Type declared on the first source-mode
// Input of a group, and true when one is present. Returns ("", false) when
// the group is nil or no source input declares a type (so the caller skips
// the type-mismatch check rather than spuriously rejecting valid inputs).
func entrySourceInputType(g *structuremap.Group) (string, bool) {
	if g == nil {
		return "", false
	}
	for _, in := range g.Input {
		if in.Mode == "source" {
			return in.Type, true
		}
	}
	return "", false
}

// isEffectivelyEmpty reports whether a JSON object has no meaningful content
// beyond the bare "resourceType" discriminator. This is the AC-3 tripwire:
// a payload like {} or {"resourceType":"Patient"} almost certainly represents
// a programming error and should fail early with a clear diagnostic rather
// than silently producing an empty target. [SYM-GR-0013]
func isEffectivelyEmpty(m map[string]any) bool {
	for k := range m {
		if k != "resourceType" {
			return false
		}
	}
	return true
}
