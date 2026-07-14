package fhirpath

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// fnSelect projects each receiver element through `expr`, flattening results, useful when authors want to derive new shapes.
func fnSelect(receiver []any, expr *Node, env map[string]any) ([]any, error) {
	var out []any
	for i, item := range receiver {
		res, err := evalWithIndex(expr, item, env, i)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
	}
	return out, nil
}

// evalWithIndex evaluates expr against item with the FHIRPath `$index`
// variable bound to i (0-based) for the duration of the call, then restores
// the prior binding. The save/restore lets nested iterators
// (`a.where(b.select(...))`) each see their own index without clobbering an
// enclosing one — the same discipline fnAggregate uses for `$total`. env may
// be nil (e.g. a top-level Eval); a fresh map is used in that case.
func evalWithIndex(expr *Node, item any, env map[string]any, i int) ([]any, error) {
	if env == nil {
		env = map[string]any{}
	}
	prev, had := env["$index"]
	env["$index"] = int64(i)
	res, err := evalIn(expr, item, env)
	if had {
		env["$index"] = prev
	} else {
		delete(env, "$index")
	}
	return res, err
}

// fnRepeat applies `expr` to each element, then to every element of the
// result, until no new elements appear. Duplicates dropped (by deep-
// equality) so cycles can't blow the loop up. Common idiom:
//
//	Questionnaire.repeat(item).linkId
//
// flattens every nested item.linkId regardless of depth.
func fnRepeat(receiver []any, expr *Node, env map[string]any) ([]any, error) {
	var out []any
	frontier := receiver
	guard := 0
	for len(frontier) > 0 {
		// Hard upper bound to keep a pathological cycle from looping
		// forever — 10 000 is well above any realistic FHIR tree.
		guard++
		if guard > 10_000 {
			return nil, fmt.Errorf("repeat() exceeded iteration limit")
		}
		var next []any
		for _, item := range frontier {
			res, err := evalIn(expr, item, env)
			if err != nil {
				return nil, err
			}
			for _, r := range res {
				if !containsDeep(out, r) {
					out = append(out, r)
					next = append(next, r)
				}
			}
		}
		frontier = next
	}
	return out, nil
}

func fnDistinct(receiver []any) []any {
	var out []any
	for _, v := range receiver {
		if !containsDeep(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func fnIsDistinct(receiver []any) bool {
	seen := make([]any, 0, len(receiver))
	for _, v := range receiver {
		if containsDeep(seen, v) {
			return false
		}
		seen = append(seen, v)
	}
	return true
}

func fnTail(receiver []any) []any {
	if len(receiver) <= 1 {
		return nil
	}
	return receiver[1:]
}

func fnSkip(receiver []any, n int) []any {
	if n <= 0 {
		return receiver
	}
	if n >= len(receiver) {
		return nil
	}
	return receiver[n:]
}

func fnTake(receiver []any, n int) []any {
	if n <= 0 {
		return nil
	}
	if n >= len(receiver) {
		return receiver
	}
	return receiver[:n]
}

// fnOfType filters by a type name. For primitives we match by Go kind
// (string/integer/decimal/boolean). For maps with a `resourceType` we
// match that string. Unknown names yield empty — that's safer than
// silently passing everything through.
func fnOfType(receiver []any, typeName string) []any {
	var out []any
	for _, v := range receiver {
		if matchesType(v, typeName) {
			out = append(out, v)
		}
	}
	return out
}

func matchesType(v any, name string) bool {
	switch strings.ToLower(name) {
	case "string", "code", "uri", "url", "canonical", "id", "markdown", "oid", "uuid":
		_, ok := v.(string)
		return ok
	case "integer", "positiveint", "unsignedint":
		switch v.(type) {
		case int, int32, int64:
			return true
		}
		return false
	case "decimal", "number":
		switch v.(type) {
		case float32, float64:
			return true
		}
		return false
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "date", "datetime", "time", "instant":
		_, ok := v.(string)
		return ok
	}
	// Resource-type match: m["resourceType"] == name. Names are
	// usually PascalCase ("Patient", "Observation"), so compare verbatim
	// against the original (not lowercased) name.
	if m, ok := v.(map[string]any); ok {
		if rt, ok := m["resourceType"].(string); ok && rt == name {
			return true
		}
	}
	return false
}

// fnAggregate is FHIRPath's reduce. Each iteration binds `$this` to the
// element (already the receiver-walk's natural subject) and `$total` to
// the running accumulator. `$total` lives on env under the reserved
// key "$total" — the identifier evaluator looks it up alongside `$this`.
// The original env value (if any) is saved and restored so nested
// aggregates don't clobber each other.
func fnAggregate(receiver []any, expr *Node, init []any, env map[string]any) ([]any, error) {
	if env == nil {
		env = map[string]any{}
	}
	prev, hadPrev := env["$total"]
	defer func() {
		if hadPrev {
			env["$total"] = prev
		} else {
			delete(env, "$total")
		}
	}()
	// Stash the accumulator as a single value when the collection has one
	// element, otherwise as the whole list. Mirrors evalIn's identifier
	// path which flattens single-element lookups.
	setTotal := func(c []any) {
		switch len(c) {
		case 0:
			env["$total"] = nil
		case 1:
			env["$total"] = c[0]
		default:
			env["$total"] = anySlice(c)
		}
	}
	acc := init
	setTotal(acc)
	for _, item := range receiver {
		res, err := evalIn(expr, item, env)
		if err != nil {
			return nil, err
		}
		acc = res
		setTotal(acc)
	}
	return acc, nil
}

// anySlice copies a []any so callers can store it without aliasing the
// caller's backing array. Defensive — aggregate threads the accumulator
// through env so a shared backing slice would corrupt successive runs.
func anySlice(c []any) []any {
	out := make([]any, len(c))
	copy(out, c)
	return out
}

// fnEndsWith returns true when any receiver element ends with `suffix`.
func fnEndsWith(receiver []any, suffix string) bool {
	for _, r := range receiver {
		if strings.HasSuffix(stringify(r), suffix) {
			return true
		}
	}
	return false
}

func fnIndexOf(receiver []any, needle string) int64 {
	if len(receiver) == 0 {
		return -1
	}
	idx := strings.Index(stringify(receiver[0]), needle)
	return int64(idx)
}

func fnTrim(receiver []any) []any {
	out := make([]any, 0, len(receiver))
	for _, r := range receiver {
		out = append(out, strings.TrimSpace(stringify(r)))
	}
	return out
}

func fnSplit(receiver []any, sep string) []any {
	if len(receiver) == 0 {
		return nil
	}
	parts := strings.Split(stringify(receiver[0]), sep)
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

func fnJoin(receiver []any, sep string) string {
	parts := make([]string, len(receiver))
	for i, r := range receiver {
		parts[i] = stringify(r)
	}
	return strings.Join(parts, sep)
}

// --- Conversion: to{Integer,Decimal,String,Boolean,Date,DateTime,Time}
//
// Each returns (value, true) on success and (zero, false) on failure.
// The dispatcher coerces the (zero, false) case to an empty collection,
// matching FHIRPath's "unconvertible → empty" rule. The convertsTo*
// variants reuse the same predicates.

func toInteger(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int32:
		return int64(x), true
	case int64:
		return x, true
	case float32:
		if float64(x) == float64(int64(x)) {
			return int64(x), true
		}
	case float64:
		if x == float64(int64(x)) {
			return int64(x), true
		}
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func toDecimal(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func toBoolean(v any) (value, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		// Spec restricts to {"true","false","t","f","1","0"}. We accept the
		// canonical FHIR strings only — "yes"/"no" etc. are non-conformant
		// and should return empty.
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "t", "1":
			return true, true
		case "false", "f", "0":
			return false, true
		}
	case int, int32, int64:
		f, _ := numericFloat(x)
		if f == 1 {
			return true, true
		}
		if f == 0 {
			return false, true
		}
	case float32, float64:
		f, _ := numericFloat(x)
		if f == 1 {
			return true, true
		}
		if f == 0 {
			return false, true
		}
	}
	return false, false
}

// Date / DateTime / Time validation accepts the FHIR primitive lexical
// forms. We don't normalise — a successful validation returns the input
// string unchanged so timezone offsets and partial precisions survive
// round-trip.

var (
	dateRE     = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
	timeRE     = regexp.MustCompile(`^\d{2}(:\d{2}(:\d{2}(\.\d+)?)?)?$`)
	dateTimeRE = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2}(T\d{2}(:\d{2}(:\d{2}(\.\d+)?)?)?(Z|[+\-]\d{2}:\d{2})?)?)?)?$`)
)

func toDate(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	if !dateRE.MatchString(s) {
		return "", false
	}
	return s, true
}

func toDateTime(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	if !dateTimeRE.MatchString(s) {
		return "", false
	}
	return s, true
}

func toTime(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	if !timeRE.MatchString(s) {
		return "", false
	}
	return s, true
}

// nowISO and todayISO call time.Now() directly.
func nowISO() string   { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }
func todayISO() string { return time.Now().UTC().Format("2006-01-02") }

// containsDeep tests membership using equalScalar for scalars and
// reflect-style equality for maps/slices. Used by distinct/isDistinct/
// repeat where the spec requires set-like semantics.
func containsDeep(haystack []any, needle any) bool {
	for _, h := range haystack {
		if deepEqual(h, needle) {
			return true
		}
	}
	return false
}

func deepEqual(a, b any) bool {
	// Maps: same keys, deep-equal values. Check before falling through to
	// equalScalar (which would panic on `a == b` for an uncomparable map).
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		if len(am) != len(bm) {
			return false
		}
		for k, v := range am {
			bv, ok := bm[k]
			if !ok || !deepEqual(v, bv) {
				return false
			}
		}
		return true
	}
	as, aok2 := a.([]any)
	bs, bok2 := b.([]any)
	if aok2 && bok2 {
		if len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !deepEqual(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	// Fall back to scalar equality — only reached for primitives that are
	// safe to compare with `==`.
	return equalScalar(a, b)
}
