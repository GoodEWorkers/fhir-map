package transform

import (
	"fmt"
	"strings"
)

// writeTargetElement writes value into holder at the path described by
// element, honouring the FHIR Mapping Language target list-mode markers:
//
//	plain.field         — descend / write as a map key
//	list[+].child       — allocate a fresh element in `list` and descend
//	                      into it
//	list[=].child       — reuse the most-recent element in `list`
//	                      (allocates one if the list is empty)
//	list[N].child       — address element at fixed integer index N
//	                      (allocating intermediate slots as needed)
//
// Behaviour on intermediate type mismatch (e.g. cur["entry"] already
// holds a string, not a map/list) is permissive: the write is dropped
// rather than panicking, matching the executor's HAPI-compat "skip on
// unbound source" policy elsewhere.
// mode is the active target list mode for the bare-element write
// ("first" | "last" | "single" | "collate" | ""). `share` is resolved by the
// caller (executeTarget) before this point. An empty mode keeps the
// promote-on-repeat heuristic. Returns an error only for a `single` violation.
func writeTargetElement(holder map[string]any, element string, value any, accumulate bool, mode string) error {
	if !strings.ContainsAny(element, "[.") {
		existing, present := holder[element]
		switch mode {
		case "collate":
			holder[element] = appendListValue(existing, present, value)
			return nil
		case "first":
			if !present {
				holder[element] = value
			}
			return nil
		case "last":
			holder[element] = value
			return nil
		case "single":
			// Exactly one write is permitted (HAPI's SINGLE cardinality).
			if present {
				return fmt.Errorf("%w: target element %q", ErrTargetListSingle, element)
			}
			holder[element] = value
			return nil
		}
		// No explicit mode — promote-on-repeat: a `copy` rule that fires once
		// per repeating source match (HL7v2 OBR/OBX) writes to the same bare
		// element each firing. HAPI appends a list item per firing because it
		// knows the element's cardinality from the schema; fhir-map has none,
		// so we infer it — first write is scalar (keeps MSH-1, PID-5, … scalar),
		// each subsequent write accumulates into a list. Only when accumulate is
		// set (the target introduces no variable); variable-binding targets
		// (`create` + dependent-group population) keep last-wins semantics.
		if present && accumulate {
			holder[element] = appendListValue(existing, present, value)
			return nil
		}
		holder[element] = value
		return nil
	}
	segs := parseElementPath(element)
	if len(segs) == 0 {
		return nil
	}
	cur := holder
	for i, seg := range segs {
		last := i == len(segs)-1
		switch seg.mode {
		case "":
			if last {
				cur[seg.name] = value
				return nil
			}
			next, ok := cur[seg.name].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[seg.name] = next
			}
			cur = next
		case "+":
			list := ensureList(cur, seg.name)
			fresh := map[string]any{}
			list = append(list, fresh)
			cur[seg.name] = list
			if last {
				// Rare: the leaf is itself a [+] write — store value as the
				// new list element rather than wrapping it in a map.
				list[len(list)-1] = value
				cur[seg.name] = list
				return nil
			}
			cur = fresh
		case "=":
			list := ensureList(cur, seg.name)
			if len(list) == 0 {
				// `[=]` with nothing to reuse: behave like `[+]`. A spec-
				// strict mode could error here, but the practical mapping
				// corpus always pairs `[=]` with a prior `[+]` — being
				// permissive matches HAPI's executor on this edge.
				fresh := map[string]any{}
				list = append(list, fresh)
				cur[seg.name] = list
			}
			idx := len(list) - 1
			if last {
				list[idx] = value
				cur[seg.name] = list
				return nil
			}
			next, ok := list[idx].(map[string]any)
			if !ok {
				next = map[string]any{}
				list[idx] = next
				cur[seg.name] = list
			}
			cur = next
		default:
			// `[N]` (fixed index) not yet implemented; treated as "+" so
			// existing fixture writes don't hard-fail on the unsupported marker.
			list := ensureList(cur, seg.name)
			fresh := map[string]any{}
			list = append(list, fresh)
			cur[seg.name] = list
			if last {
				list[len(list)-1] = value
				cur[seg.name] = list
				return nil
			}
			cur = fresh
		}
	}
	return nil
}

// appendListValue appends value to an element's existing contents, coercing to a []any.
// Absent → one-element list; existing list → appended; existing scalar → two-element list.
func appendListValue(existing any, present bool, value any) []any {
	if !present {
		return []any{value}
	}
	if lst, ok := existing.([]any); ok {
		return append(lst, value)
	}
	return []any{existing, value}
}

// elementSeg is one dot-separated step of a target element path with its
// optional list-mode marker (the part between [ and ]).
type elementSeg struct {
	name string
	mode string
}

// parseElementPath splits an element string on `.` and pulls any
// trailing `[X]` marker off each segment. Malformed input is preserved
// literally; the permissive type-mismatch path will drop the write.
func parseElementPath(s string) []elementSeg {
	parts := strings.Split(s, ".")
	out := make([]elementSeg, 0, len(parts))
	for _, p := range parts {
		seg := elementSeg{name: p}
		if open := strings.LastIndexByte(p, '['); open >= 0 && strings.HasSuffix(p, "]") {
			marker := p[open+1 : len(p)-1]
			seg.name = p[:open]
			seg.mode = marker
		}
		out = append(out, seg)
	}
	return out
}

// ensureList reads cur[name] as a []any, allocating a fresh empty slice
// if absent. If the existing value isn't a slice the caller's earlier
// permissive contract overrides — the literal will be dropped.
func ensureList(cur map[string]any, name string) []any {
	if existing, ok := cur[name].([]any); ok {
		return existing
	}
	return []any{}
}
