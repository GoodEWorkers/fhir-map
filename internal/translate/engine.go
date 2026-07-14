// Package translate implements the FHIR $translate operation for ConceptMap resources.
package translate

import (
	"context"
	"fmt"
	"strings"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Request represents the input parameters for a $translate operation.
type Request struct {
	// Identify the ConceptMap
	URL          string
	ConceptMapID string
	ConceptMap   *conceptmap.ConceptMap // Inline ConceptMap
	Version      string

	// Source concept (one of these must be provided)
	SourceCode            string
	SourceSystem          string
	SourceVersion         string
	SourceCoding          *fhir.Coding
	SourceCodeableConcept *fhir.CodeableConcept

	// Target concept (for reverse translation)
	TargetCode            string
	TargetCoding          *fhir.Coding
	TargetCodeableConcept *fhir.CodeableConcept

	// Scope
	SourceScope  string
	TargetScope  string
	TargetSystem string

	// Dependencies carries the FHIR R5 `dependency` input parameter. Each
	// entry constrains which target rows are eligible for the translation by
	// matching against the stored dependsOn list on each target.
	Dependencies []DependencyInput
}

// DependencyInput mirrors a single $translate `dependency` input parameter
// (FHIR R5 ConceptMap/$translate operation definition).
type DependencyInput struct {
	Attribute   string
	ValueCode   string
	ValueString string
	ValueCoding *fhir.Coding
}

// Translator is the engine surface used by HTTP handlers. The production
// implementation is *Engine; tests can substitute a stub to inspect the
// request before it reaches the real translate pipeline.
type Translator interface {
	Translate(ctx context.Context, req Request) (*Response, error)
}

// Response represents the output of a $translate operation.
type Response struct {
	Result  bool
	Message string
	Matches []fhir.TranslateMatch
	// IsError is true when this Response represents an engine-level failure
	// (e.g. depth-cap exceeded, store error) rather than a legitimate
	// "no mapping found" outcome. Callers should not emit an `unmapped`
	// echo part when IsError=true — the probe failed, it was not simply unmapped.
	IsError bool
	// Warnings carry non-fatal advisories the engine produces while servicing
	// the request, such as a `dependency.attribute` that names no declared
	// target attribute. Pass-through behaviour is preserved; the warning is
	// purely informational and surfaces in the $translate response as one
	// OperationOutcome-shaped `issue` parameter per Warning.
	Warnings []Warning
}

// Warning is a non-fatal advisory attached to a translate Response.
type Warning struct {
	Code        string
	Diagnostics string
}

// Engine performs $translate operations using a ConceptMap repository.
type Engine struct {
	repo conceptmap.Repository
}

// NewEngine creates a new translate engine.
func NewEngine(repo conceptmap.Repository) *Engine {
	return &Engine{repo: repo}
}

// otherMapMaxDepth caps how deep an `unmapped.mode = other-map` chain can recurse
// before the engine bails out. Anything deeper is almost certainly a cycle and
// would either hang or stack-overflow.
const otherMapMaxDepth = 5

// Translate executes the $translate operation.
func (e *Engine) Translate(ctx context.Context, req Request) (*Response, error) {
	return e.translateWithDepth(ctx, req, 0)
}

// translateWithDepth carries the other-map recursion counter through chained
// fallback translations. Direct callers should use Translate; the engine itself
// re-enters here from handleUnmapped when chasing `unmapped.mode = other-map`.
func (e *Engine) translateWithDepth(ctx context.Context, req Request, depth int) (*Response, error) {
	// Parse pipe-delimited version from URL (e.g., "http://example.org/cm|1.0")
	req = parsePipeVersion(req)

	// Resolve the ConceptMap to use
	cm, err := e.resolveConceptMap(ctx, req)
	if err != nil {
		return nil, err
	}

	// Determine if this is a reverse (target->source) translation
	targetCode, targetSystem, isReverse := resolveTargetConcept(req)

	var matches []fhir.TranslateMatch

	if isReverse {
		matches = e.translateReverse(cm, targetCode, targetSystem, req.TargetSystem)
	} else {
		// Handle multi-coding CodeableConcept by translating ALL codings
		codings := resolveAllSourceCodings(req)
		if len(codings) == 0 {
			// No source concepts at all - translate with empty (may trigger unmapped)
			matches, err = e.translateForwardWithUnmapped(ctx, cm, "", "", req.TargetSystem, req.Dependencies, depth)
			if err != nil {
				return nil, err
			}
		} else {
			for _, coding := range codings {
				partial, err := e.translateForwardWithUnmapped(ctx, cm, coding.Code, coding.System, req.TargetSystem, req.Dependencies, depth)
				if err != nil {
					return nil, err
				}
				matches = append(matches, partial...)
			}
		}
	}

	// Deduplicate matches by (code, relationship, system)
	matches = deduplicateMatches(matches)

	// Determine result: true if at least one match has relationship != "not-related-to"
	result := false
	hasAnyMatch := len(matches) > 0
	for _, m := range matches {
		if m.Relationship != "not-related-to" {
			result = true
			break
		}
	}

	resp := &Response{
		Result:  result,
		Matches: matches,
	}

	// Improved negative match messaging
	if !result {
		if hasAnyMatch {
			resp.Message = "Only negative matches found"
		} else {
			resp.Message = "No mapping found for the provided concept"
		}
	}

	// Emit one warning per requested `dependency.attribute` that names no
	// declared target attribute anywhere in the active ConceptMap.
	// Pass-through behaviour is preserved; the warning is additive.
	if len(req.Dependencies) > 0 {
		resp.Warnings = append(resp.Warnings, collectUnmatchedDependencyWarnings(cm, req.Dependencies)...)
	}

	return resp, nil
}

// collectUnmatchedDependencyWarnings returns one Warning per requested dependency
// whose Attribute does not appear in any target's DependsOn declaration across
// the active ConceptMap. Order follows req.Dependencies; duplicates within the
// request produce duplicate warnings (the request shape is the source of truth).
func collectUnmatchedDependencyWarnings(cm *conceptmap.ConceptMap, deps []DependencyInput) []Warning {
	if cm == nil || len(deps) == 0 {
		return nil
	}
	declaredAttrs := map[string]struct{}{}
	for _, group := range cm.Group {
		for _, element := range group.Element {
			for ti := range element.Target {
				target := &element.Target[ti]
				for _, dep := range target.DependsOn {
					if dep.Attribute != "" {
						declaredAttrs[dep.Attribute] = struct{}{}
					}
				}
			}
		}
	}
	var warnings []Warning
	emitted := map[string]struct{}{}
	for _, d := range deps {
		if d.Attribute == "" {
			continue
		}
		if _, ok := declaredAttrs[d.Attribute]; ok {
			continue
		}
		if _, dup := emitted[d.Attribute]; dup {
			continue
		}
		emitted[d.Attribute] = struct{}{}
		warnings = append(warnings, Warning{
			Code:        "not-supported",
			Diagnostics: fmt.Sprintf("dependency attribute %q matched no target's dependsOn", d.Attribute),
		})
	}
	return warnings
}

// parsePipeVersion extracts version from pipe-delimited URL (e.g., "http://example.org/cm|1.0").
func parsePipeVersion(req Request) Request {
	if req.URL != "" && strings.Contains(req.URL, "|") {
		parts := strings.SplitN(req.URL, "|", 2)
		req.URL = parts[0]
		if req.Version == "" && len(parts) > 1 {
			req.Version = parts[1]
		}
	}
	return req
}

// resolveAllSourceCodings extracts ALL source codings from the request.
// For CodeableConcept, returns all codings (not just the first).
func resolveAllSourceCodings(req Request) []fhir.Coding {
	if req.SourceCode != "" {
		return []fhir.Coding{{System: req.SourceSystem, Code: req.SourceCode}}
	}
	if req.SourceCoding != nil {
		return []fhir.Coding{*req.SourceCoding}
	}
	if req.SourceCodeableConcept != nil && len(req.SourceCodeableConcept.Coding) > 0 {
		return req.SourceCodeableConcept.Coding
	}
	return nil
}

// deduplicateMatches removes duplicate matches with the same (code, relationship, system).
func deduplicateMatches(matches []fhir.TranslateMatch) []fhir.TranslateMatch {
	if len(matches) <= 1 {
		return matches
	}

	type matchKey struct {
		Code         string
		Relationship string
		System       string
	}

	seen := make(map[matchKey]bool)
	var result []fhir.TranslateMatch

	for _, m := range matches {
		key := matchKey{
			Relationship: m.Relationship,
		}
		if m.Concept != nil {
			key.Code = m.Concept.Code
			key.System = m.Concept.System
		}

		if !seen[key] {
			seen[key] = true
			result = append(result, m)
		}
	}

	return result
}

// resolveConceptMap determines which ConceptMap to use for translation.
// It preserves domain sentinel errors (ErrGone, ErrNotFound) so callers
// can map them to the correct HTTP status codes (410, 404 respectively).
func (e *Engine) resolveConceptMap(ctx context.Context, req Request) (*conceptmap.ConceptMap, error) {
	// If an inline ConceptMap was provided, use it directly
	if req.ConceptMap != nil {
		return req.ConceptMap, nil
	}

	// If a specific ID was given, fetch by ID. Propagate the repo error as-is
	// so the handler can distinguish ErrGone (410), ErrNotFound (404), and
	// transient backend failures (5xx) by their sentinel identity.
	if req.ConceptMapID != "" {
		return e.repo.Read(ctx, req.ConceptMapID)
	}

	// If a URL was given, fetch by URL — same error-propagation contract.
	if req.URL != "" {
		return e.repo.FindByURL(ctx, req.URL, req.Version)
	}

	// If source scope is provided, try to resolve by scope
	if req.SourceScope != "" {
		cm, err := e.repo.FindBySourceScope(ctx, req.SourceScope)
		if err == nil {
			return cm, nil
		}
		// Fall through to error if not found
	}

	return nil, fmt.Errorf("either url, conceptMap, or a specific ConceptMap ID must be provided")
}

// resolveTargetConcept extracts the target code/system for reverse translation.
func resolveTargetConcept(req Request) (code, system string, isReverse bool) {
	if req.TargetCode != "" {
		return req.TargetCode, req.TargetSystem, true
	}
	if req.TargetCoding != nil {
		return req.TargetCoding.Code, req.TargetCoding.System, true
	}
	if req.TargetCodeableConcept != nil && len(req.TargetCodeableConcept.Coding) > 0 {
		c := req.TargetCodeableConcept.Coding[0]
		return c.Code, c.System, true
	}
	return "", "", false
}

// translateForwardWithUnmapped is the recursion-aware forward path. depth is
// the number of `other-map` hops taken so far; once it reaches otherMapMaxDepth
// the engine refuses to recurse further (cycle protection).
func (e *Engine) translateForwardWithUnmapped(ctx context.Context, cm *conceptmap.ConceptMap, sourceCode, sourceSystem, targetSystemFilter string, deps []DependencyInput, depth int) ([]fhir.TranslateMatch, error) {
	var matches []fhir.TranslateMatch

	for _, group := range cm.Group {
		// Skip groups that don't match the source system (if specified)
		if sourceSystem != "" && group.Source != "" && group.Source != sourceSystem {
			continue
		}
		// Skip groups that don't match the target system filter (if specified)
		if targetSystemFilter != "" && group.Target != "" && group.Target != targetSystemFilter {
			continue
		}

		found := false
		for _, element := range group.Element {
			if element.Code != sourceCode {
				continue
			}
			found = true

			// When the caller supplies `dependency` inputs, drop targets whose
			// stored DependsOn list does not satisfy every requested
			// attribute/value. Targets with no DependsOn at all are also
			// dropped — the request is explicitly narrowing the result set.
			eligible := filterTargetsByDependencies(element.Target, deps)
			for ti := range eligible {
				target := &eligible[ti]
				match := fhir.TranslateMatch{
					Relationship: target.Relationship,
					Concept: &fhir.Coding{
						System:  group.Target,
						Code:    target.Code,
						Display: target.Display,
					},
					OriginMap: cm.URL,
				}
				// Include dependsOn/product in response with the correct value[x]
				// type — ValueCoding wins over ValueCode wins over ValueString to
				// match the FHIR choice ordering.
				for _, dep := range target.DependsOn {
					match.DependsOn = append(match.DependsOn, toMatchDependency(dep))
				}
				for _, prod := range target.Product {
					match.Product = append(match.Product, toMatchDependency(prod))
				}
				matches = append(matches, match)
			}
		}

		// Handle unmapped concepts
		if !found && group.Unmapped != nil {
			unmappedMatches, err := e.applyUnmapped(ctx, group.Unmapped, sourceCode, sourceSystem, group.Target, cm.URL, targetSystemFilter, depth)
			if err != nil {
				return nil, err
			}
			matches = append(matches, unmappedMatches...)
		}
	}

	return matches, nil
}

// toMatchDependency converts a stored DependsOn/Product into the response shape,
// preserving whichever value[x] the source set.
func toMatchDependency(dep conceptmap.DependsOn) fhir.TranslateMatchDependency {
	return fhir.TranslateMatchDependency{
		Attribute:   dep.Attribute,
		ValueString: dep.ValueString,
		ValueCode:   dep.ValueCode,
		ValueCoding: dep.ValueCoding,
	}
}

// filterTargetsByDependencies narrows a target slice by the caller-supplied
// `dependency` $translate inputs. The caller's intent is to constrain which
// stored target rows are eligible; an empty deps slice means no filtering.
//
// A target survives only if, for every requested dependency, either (a) the
// target declares a DependsOn entry with that attribute whose value matches,
// or (b) the target declares no DependsOn entry for that attribute at all
// (unconstrained — applies regardless of the requested value). A target that
// declares the attribute but with a non-matching value is dropped.
func filterTargetsByDependencies(targets []conceptmap.Target, deps []DependencyInput) []conceptmap.Target {
	if len(deps) == 0 {
		return targets
	}
	out := make([]conceptmap.Target, 0, len(targets))
	for ti := range targets {
		if targetSatisfiesAllDeps(targets[ti], deps) {
			out = append(out, targets[ti])
		}
	}
	return out
}

func targetSatisfiesAllDeps(t conceptmap.Target, deps []DependencyInput) bool {
	for _, d := range deps {
		if !targetHasMatchingDep(t, d) {
			return false
		}
	}
	return true
}

// targetHasMatchingDep reports whether target t satisfies the single dependency d.
// Matching is tried in priority order: ValueCoding (system+code), ValueCode,
// ValueString. A DependencyInput where all three value fields are zero/nil will
// never match — the handler's dependency-parsing path is expected to drop such
// entries before they reach the engine (see parseTranslateRequest).
//
// A target that declares no DependsOn entry for d.Attribute is treated as
// unconstrained and matches — per FHIR, the dependency narrows the set by
// excluding targets that explicitly conflict, not by requiring every target
// to opt in.
func targetHasMatchingDep(t conceptmap.Target, d DependencyInput) bool {
	declaresAttr := false
	for _, td := range t.DependsOn {
		if td.Attribute != d.Attribute {
			continue
		}
		declaresAttr = true
		switch {
		case d.ValueCoding != nil:
			if td.ValueCoding != nil &&
				td.ValueCoding.Code == d.ValueCoding.Code &&
				td.ValueCoding.System == d.ValueCoding.System {
				return true
			}
		case d.ValueCode != "":
			if td.ValueCode == d.ValueCode || td.ValueString == d.ValueCode {
				return true
			}
		case d.ValueString != "":
			if td.ValueString == d.ValueString || td.ValueCode == d.ValueString {
				return true
			}
		}
	}
	return !declaresAttr
}

// translateReverse translates from target back to source (reverse lookup).
//
// `targetSystem` is the system of the inbound target code being reverse-resolved
// (filters which group.target the lookup occurs against). `targetSystemFilter`
// is the caller-supplied `targetSystem` $translate parameter — for a reverse
// lookup this constrains which group.target the result must come from, mirroring
// the forward path. Previously this argument was accepted but ignored.
func (e *Engine) translateReverse(cm *conceptmap.ConceptMap, targetCode, targetSystem, targetSystemFilter string) []fhir.TranslateMatch {
	var matches []fhir.TranslateMatch

	for _, group := range cm.Group {
		// For reverse, the target system of the group is the source of input
		if targetSystem != "" && group.Target != "" && group.Target != targetSystem {
			continue
		}
		// Also honour the caller's explicit targetSystem filter on reverse.
		if targetSystemFilter != "" && group.Target != "" && group.Target != targetSystemFilter {
			continue
		}

		for _, element := range group.Element {
			for ti := range element.Target {
				target := &element.Target[ti]
				if target.Code == targetCode {
					match := fhir.TranslateMatch{
						Relationship: reverseRelationship(target.Relationship),
						Concept: &fhir.Coding{
							System:  group.Source,
							Code:    element.Code,
							Display: element.Display,
						},
						OriginMap: cm.URL,
					}
					matches = append(matches, match)
				}
			}
		}
	}

	return matches
}

// applyUnmapped produces zero-or-more matches for a source code that no group
// element matched, using the group's unmapped strategy.
//
// `fixed` and `use-source-code` produce one synthetic match each. `other-map`
// re-enters Translate against the chained ConceptMap (depth-counted to defeat
// cycles in the official examples corpus).
func (e *Engine) applyUnmapped(ctx context.Context, unmapped *conceptmap.Unmapped, sourceCode, sourceSystem, targetSystem, originMap, targetSystemFilter string, depth int) ([]fhir.TranslateMatch, error) {
	switch unmapped.Mode {
	case "fixed":
		return []fhir.TranslateMatch{{
			Relationship: unmapped.Relationship,
			Concept: &fhir.Coding{
				System:  targetSystem,
				Code:    unmapped.Code,
				Display: unmapped.Display,
			},
			OriginMap: originMap,
		}}, nil
	case "use-source-code":
		return []fhir.TranslateMatch{{
			Relationship: unmapped.Relationship,
			Concept: &fhir.Coding{
				System: targetSystem,
				Code:   sourceCode,
			},
			OriginMap: originMap,
		}}, nil
	case "other-map":
		if depth+1 >= otherMapMaxDepth {
			// PHI: do not include sourceCode in the error string. The error
			// propagates to handler logger.Error("translate error", ...) where
			// the code value would leak into server logs at every log level.
			// The client still receives the full OperationOutcome via
			// writeOperationOutcome; the server log only needs the category.
			return nil, fmt.Errorf("other-map recursion limit exceeded (depth %d)", otherMapMaxDepth)
		}
		if unmapped.OtherMap == "" {
			return nil, nil
		}
		nextResp, err := e.translateWithDepth(ctx, Request{
			URL:          unmapped.OtherMap,
			SourceCode:   sourceCode,
			SourceSystem: sourceSystem,
			TargetSystem: targetSystemFilter,
		}, depth+1)
		if err != nil {
			return nil, err
		}
		return nextResp.Matches, nil
	default:
		return nil, nil
	}
}

// reverseRelationship flips the directionality of a relationship code.
func reverseRelationship(rel string) string {
	switch rel {
	case "source-is-narrower-than-target":
		return "source-is-broader-than-target"
	case "source-is-broader-than-target":
		return "source-is-narrower-than-target"
	default:
		return rel // equivalent, related-to, not-related-to are symmetric
	}
}
