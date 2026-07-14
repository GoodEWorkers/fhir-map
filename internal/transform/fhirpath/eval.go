package fhirpath

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

// Eval parses and evaluates a FHIRPath expression against `subject`,
// returning the resulting collection.
//
// Subjects are expected to be JSON-shaped values: maps, slices, strings,
// numbers, bools. The evaluator does no JSON parsing of its own — feed it
// what `encoding/json` decoded into `any`. Typed Go structs work too as
// long as their JSON tags match the FHIRPath identifiers used (the
// executor handles this by marshalling/unmarshalling once at the
// transform boundary).
//
// Returns are always slices of `any`. FHIRPath has no scalar/list
// distinction at the value level — a missing path is an empty slice, not
// nil; a single match is a one-element slice.
func Eval(expr string, subject any) ([]any, error) {
	return EvalIn(expr, subject, nil)
}

// EvalIn is Eval plus a bindings env, used to resolve `%name` external
// constants. Pass nil when no external constants are referenced.
// The env keys are bare names (no leading %); values are JSON-shaped (a
// scalar, map, or slice).
func EvalIn(expr string, subject any, env map[string]any) ([]any, error) {
	node, err := parse(expr)
	if err != nil {
		return nil, err
	}
	return evalIn(node, subject, env)
}

func evalIn(node *Node, subject any, env map[string]any) ([]any, error) {
	switch node.Kind {
	case nodeString:
		return []any{node.Text}, nil
	case nodeInteger:
		return []any{node.IntValue}, nil
	case nodeDecimal:
		// Decimal literals are float64 — consistent with FHIR JSON decimals
		// (json.Unmarshal yields float64) and the evaluator's numeric ops.
		return []any{node.FloatValue}, nil
	case nodeBool:
		return []any{node.BoolValue}, nil
	case nodeIdentifier:
		// $this refers to the current evaluation context — return it as a
		// single-element collection so chained navigation works naturally
		// (e.g. `$this.exists()`, `$this = 'ORU'`).
		if node.Text == "$this" {
			if subject == nil {
				return nil, nil
			}
			return []any{subject}, nil
		}
		// $total is the aggregate accumulator, bound by fnAggregate under
		// the reserved env key so nested aggregates can stack without
		// colliding with user-bound %name constants.
		if node.Text == "$total" {
			v, ok := env["$total"]
			if !ok || v == nil {
				return nil, nil
			}
			return flatten(v), nil
		}
		// $index — the 0-based position of the current item inside an
		// expression-parameter iterator (where/select/exists). Bound per
		// iteration under the reserved env key "$index" (see evalWithIndex);
		// empty outside any iterator, per FHIRPath's missing = empty rule.
		if node.Text == "$index" {
			v, ok := env["$index"]
			if !ok || v == nil {
				return nil, nil
			}
			return []any{v}, nil
		}
		return navigate(subject, node.Text), nil
	case nodeExtConst:
		// %name resolves via env; unbound names yield empty (FHIRPath's
		// "missing = empty" convention), so guard conditions like
		// `%foo.exists()` work without checking if foo is bound.
		v, ok := env[node.Text]
		if !ok || v == nil {
			return nil, nil
		}
		return flatten(v), nil
	case nodePath:
		current := []any{subject}
		for i, seg := range node.Args {
			if i == 0 {
				out, err := evalIn(seg, subject, env)
				if err != nil {
					return nil, err
				}
				current = out
				continue
			}
			var next []any
			for _, item := range current {
				out, err := evalIn(seg, item, env)
				if err != nil {
					return nil, err
				}
				next = append(next, out...)
			}
			current = next
		}
		return current, nil
	case nodeFunc:
		return evalFunc(node, subject, env)
	case nodeEq:
		return evalEq(node, subject, env)
	case nodeAnd:
		return evalAnd(node, subject, env)
	case nodeOr:
		return evalOr(node, subject, env)
	case nodeNot:
		inner, err := evalIn(node.Args[0], subject, env)
		if err != nil {
			return nil, err
		}
		return []any{!truthy(inner)}, nil
	case nodeUnion:
		// Each operand contributes its elements to the resulting collection,
		// with scalars becoming one-element contributions per FHIRPath's
		// universal-collection model.
		var out []any
		for _, arg := range node.Args {
			res, err := evalIn(arg, subject, env)
			if err != nil {
				return nil, err
			}
			out = append(out, res...)
		}
		return out, nil
	case nodeIn:
		// True if any element of a appears in b.
		return evalMembership(node, subject, env, false)
	case nodeContainsOp:
		// Inverse of `in`.
		return evalMembership(node, subject, env, true)
	case nodeCmp:
		// Dispatches on operator text.
		return evalCmp(node, subject, env)
	case nodeArith:
		// Dispatches on operator text.
		return evalArith(node, subject, env)
	case nodeIndex:
		// expr[N]: select the 0-based N-th item from receiver; out-of-range yields empty.
		recv, err := evalIn(node.Args[0], subject, env)
		if err != nil {
			return nil, err
		}
		if node.IntValue < 0 || node.IntValue >= int64(len(recv)) {
			return nil, nil
		}
		return []any{recv[int(node.IntValue)]}, nil
	case nodeComponent:
		// HL7v2 component/subcomponent: split on separator and take 1-based piece.
		recv, err := evalIn(node.Args[0], subject, env)
		if err != nil {
			return nil, err
		}
		var out []any
		for _, item := range recv {
			s, ok := item.(string)
			if !ok {
				continue
			}
			parts := strings.Split(s, node.Text)
			if node.IntValue >= 1 && node.IntValue <= int64(len(parts)) {
				if i := int(node.IntValue) - 1; parts[i] != "" {
					out = append(out, parts[i])
				}
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown node kind %d", node.Kind)
}

// evalCmp evaluates an inequality / not-equal / equivalence comparison.
// FHIRPath returns empty (treated as false here) when either operand is
// empty; numeric comparisons use float64 coercion; ~/!~ use case-
// insensitive string equality and identity-equality elsewhere.
func evalCmp(node *Node, subject any, env map[string]any) ([]any, error) {
	left, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	right, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	if len(left) == 0 || len(right) == 0 {
		return []any{false}, nil
	}
	l, r := left[0], right[0]
	switch node.Text {
	case "!=":
		return []any{!equalScalar(l, r)}, nil
	case "~":
		return []any{equivalentScalar(l, r)}, nil
	case "!~":
		return []any{!equivalentScalar(l, r)}, nil
	case "<", "<=", ">", ">=":
		lf, lok := numericFloat(l)
		rf, rok := numericFloat(r)
		if lok && rok {
			return []any{compareFloats(lf, rf, node.Text)}, nil
		}
		// String ordering as a fallback (lexicographic).
		ls, ok1 := l.(string)
		rs, ok2 := r.(string)
		if ok1 && ok2 {
			return []any{compareStrings(ls, rs, node.Text)}, nil
		}
		return []any{false}, nil
	}
	return nil, fmt.Errorf("unsupported comparison %q", node.Text)
}

func compareFloats(a, b float64, op string) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

func compareStrings(a, b, op string) bool {
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

// equivalentScalar is the `~` operator's element-equality. For strings
// it's case-insensitive; for everything else it falls back to
// equalScalar. Spec also defines coding-equivalence rules; that's a
// future iteration when we wire ConceptMap into FHIRPath.
func equivalentScalar(a, b any) bool {
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return strings.EqualFold(as, bs)
		}
	}
	return equalScalar(a, b)
}

// evalArith implements +, -, *, /, mod, div, &. Numerics flow through
// float64 (with int64 preservation when both operands are integer);
// `&` always coerces to string concat. Either operand being empty
// yields empty per FHIRPath spec.
func evalArith(node *Node, subject any, env map[string]any) ([]any, error) {
	left, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	right, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	if len(left) == 0 || len(right) == 0 {
		return nil, nil
	}
	l, r := left[0], right[0]
	if node.Text == "&" {
		return []any{stringify(l) + stringify(r)}, nil
	}
	lf, lok := numericFloat(l)
	rf, rok := numericFloat(r)
	if !lok || !rok {
		return nil, fmt.Errorf("arithmetic %q requires numeric operands; got %T and %T", node.Text, l, r)
	}
	var result float64
	switch node.Text {
	case "+":
		result = lf + rf
	case "-":
		result = lf - rf
	case "*":
		result = lf * rf
	case "/":
		if rf == 0 {
			return nil, nil
		}
		result = lf / rf
	case "mod":
		if rf == 0 {
			return nil, nil
		}
		result = float64(int64(lf) % int64(rf))
	case "div":
		if rf == 0 {
			return nil, nil
		}
		result = float64(int64(lf) / int64(rf))
	default:
		return nil, fmt.Errorf("unsupported arithmetic op %q", node.Text)
	}
	// Preserve int64 when both inputs are integer-typed and the result
	// has no fractional part — mapping authors expect `5 - 2 = 3` to
	// produce an int, not a float.
	if isInteger(l) && isInteger(r) && result == float64(int64(result)) {
		return []any{int64(result)}, nil
	}
	return []any{result}, nil
}

func isInteger(v any) bool {
	switch v.(type) {
	case int, int32, int64:
		return true
	}
	return false
}

// evalMembership backs both `in` and `contains`. With inverse=false the
// left side is the candidate(s) and the right side is the haystack
// (matching the `a in b` shape). With inverse=true the sides swap to
// match `a contains b`. Either side can be a scalar or a collection.
func evalMembership(node *Node, subject any, env map[string]any, inverse bool) ([]any, error) {
	left, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	right, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	needles, haystack := left, right
	if inverse {
		needles, haystack = right, left
	}
	if len(needles) == 0 {
		// Spec: missing operand → empty. We coerce to false so the
		// expression is usable inside where()/condition.
		return []any{false}, nil
	}
	for _, n := range needles {
		for _, h := range haystack {
			if equalScalar(n, h) {
				return []any{true}, nil
			}
		}
	}
	return []any{false}, nil
}

// navigate extracts a named field from a JSON-shaped subject, with autoflatten for lists.
func navigate(subject any, name string) []any {
	if subject == nil {
		return nil
	}
	switch s := subject.(type) {
	case map[string]any:
		if rt, ok := s["resourceType"].(string); ok && rt == name {
			return []any{s}
		}
		v, ok := s[name]
		if !ok {
			return nil
		}
		return flatten(v)
	case []any:
		var out []any
		for _, item := range s {
			out = append(out, navigate(item, name)...)
		}
		return out
	}
	return nil
}

// flatten coerces a value into a []any. A list passes through; a scalar
// becomes a singleton slice. Matches FHIRPath's universal-collection model.
func flatten(v any) []any {
	if list, ok := v.([]any); ok {
		return list
	}
	return []any{v}
}

// evalFunc dispatches the function suite.
func evalFunc(node *Node, subject any, env map[string]any) ([]any, error) {
	receiver, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	switch node.Text {
	case "where":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".where() takes exactly one argument")
		}
		filter := node.Args[1]
		var out []any
		for i, item := range receiver {
			res, err := evalWithIndex(filter, item, env, i)
			if err != nil {
				return nil, err
			}
			if truthy(res) {
				out = append(out, item)
			}
		}
		return out, nil
	case "exists":
		// .exists(predicate) filters then existence-checks; no-arg form checks non-empty.
		if len(node.Args) >= 2 {
			filter := node.Args[1]
			for i, item := range receiver {
				res, err := evalWithIndex(filter, item, env, i)
				if err != nil {
					return nil, err
				}
				if truthy(res) {
					return []any{true}, nil
				}
			}
			return []any{false}, nil
		}
		return []any{len(receiver) > 0}, nil
	case "not":
		// Method form of the not() prefix operator.
		return []any{!truthy(receiver)}, nil
	case "empty":
		return []any{len(receiver) == 0}, nil
	case "first":
		if len(receiver) == 0 {
			return nil, nil
		}
		return []any{receiver[0]}, nil
	case "last":
		if len(receiver) == 0 {
			return nil, nil
		}
		return []any{receiver[len(receiver)-1]}, nil
	case "count":
		return []any{int64(len(receiver))}, nil
	case "startsWith":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".startsWith() takes one argument")
		}
		argRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		needle := toString(argRes)
		for _, r := range receiver {
			if strings.HasPrefix(toString([]any{r}), needle) {
				return []any{true}, nil
			}
		}
		return []any{false}, nil
	case "contains":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".contains() takes one argument")
		}
		argRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		needle := toString(argRes)
		for _, r := range receiver {
			if strings.Contains(toString([]any{r}), needle) {
				return []any{true}, nil
			}
		}
		return []any{false}, nil
	case "toString":
		if len(receiver) == 0 {
			return nil, nil
		}
		return []any{toString(receiver)}, nil
	case "replace":
		if len(node.Args) != 3 {
			return nil, fmt.Errorf(".replace(pattern, replacement) takes two arguments")
		}
		pat, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		repl, err := evalIn(node.Args[2], subject, env)
		if err != nil {
			return nil, err
		}
		var out []any
		for _, r := range receiver {
			out = append(out, strings.ReplaceAll(toString([]any{r}), toString(pat), toString(repl)))
		}
		return out, nil
	case "iif":
		// iif(condition, thenExpr, elseExpr); receiver is ignored.
		if len(node.Args) < 3 {
			return nil, fmt.Errorf("iif requires (condition, then [, else])")
		}
		cond, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		branch := node.Args[3]
		if truthy(cond) {
			branch = node.Args[2]
		} else if len(node.Args) < 4 {
			// No else branch supplied — return empty.
			return nil, nil
		}
		return evalIn(branch, subject, env)
	case "length":
		// String length of the receiver's first element.
		if len(receiver) == 0 {
			return nil, nil
		}
		return []any{int64(len(stringify(receiver[0])))}, nil
	case "substring":
		// substring(start [, length]): 0-based start; out-of-range returns empty.
		if len(receiver) == 0 || len(node.Args) < 2 {
			return nil, nil
		}
		startRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		startF, ok := numericFloat(firstOf(startRes))
		if !ok {
			return nil, fmt.Errorf(".substring: start must be an integer")
		}
		s := stringify(receiver[0])
		start := intArg(startF)
		if start < 0 || start >= len(s) {
			return nil, nil
		}
		end := len(s)
		if len(node.Args) >= 3 {
			lenRes, err := evalIn(node.Args[2], subject, env)
			if err != nil {
				return nil, err
			}
			lenF, ok := numericFloat(firstOf(lenRes))
			if !ok {
				return nil, fmt.Errorf(".substring: length must be an integer")
			}
			end = start + intArg(lenF)
			if end > len(s) {
				end = len(s)
			}
		}
		return []any{s[start:end]}, nil
	case "matches":
		// matches(regex) returns boolean.
		if len(receiver) == 0 || len(node.Args) < 2 {
			return []any{false}, nil
		}
		patRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		pat := toString(patRes)
		re, err := regexp.Compile(pat)
		if err != nil {
			return nil, fmt.Errorf(".matches: invalid regex %q: %w", pat, err)
		}
		return []any{re.MatchString(stringify(receiver[0]))}, nil
	case "select":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".select() takes one argument")
		}
		return fnSelect(receiver, node.Args[1], env)
	case "repeat":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".repeat() takes one argument")
		}
		return fnRepeat(receiver, node.Args[1], env)
	case "distinct":
		return fnDistinct(receiver), nil
	case "isDistinct":
		return []any{fnIsDistinct(receiver)}, nil
	case "tail":
		return fnTail(receiver), nil
	case "skip":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".skip(n) takes one argument")
		}
		nRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		nf, ok := numericFloat(firstOf(nRes))
		if !ok {
			return nil, fmt.Errorf(".skip: argument must be an integer")
		}
		return fnSkip(receiver, intArg(nf)), nil
	case "take":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".take(n) takes one argument")
		}
		nRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		nf, ok := numericFloat(firstOf(nRes))
		if !ok {
			return nil, fmt.Errorf(".take: argument must be an integer")
		}
		return fnTake(receiver, intArg(nf)), nil
	case "ofType":
		// `ofType(name)` takes a type identifier, not an expression — the
		// arg's identifier text is the type name. Reading node.Args[1].Text
		// directly avoids the navigator trying to look up a field of that
		// name on the subject.
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".ofType(type) takes one argument")
		}
		arg := node.Args[1]
		if arg.Kind != nodeIdentifier {
			return nil, fmt.Errorf(".ofType: argument must be a type identifier")
		}
		return fnOfType(receiver, arg.Text), nil
	case "aggregate":
		if len(node.Args) < 2 {
			return nil, fmt.Errorf(".aggregate(expr [, init]) takes one or two arguments")
		}
		var init []any
		if len(node.Args) >= 3 {
			res, err := evalIn(node.Args[2], subject, env)
			if err != nil {
				return nil, err
			}
			init = res
		}
		return fnAggregate(receiver, node.Args[1], init, env)
	case "endsWith":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".endsWith() takes one argument")
		}
		argRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		return []any{fnEndsWith(receiver, toString(argRes))}, nil
	case "indexOf":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".indexOf() takes one argument")
		}
		argRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		return []any{fnIndexOf(receiver, toString(argRes))}, nil
	case "trim":
		return fnTrim(receiver), nil
	case "split":
		if len(node.Args) != 2 {
			return nil, fmt.Errorf(".split(sep) takes one argument")
		}
		argRes, err := evalIn(node.Args[1], subject, env)
		if err != nil {
			return nil, err
		}
		return fnSplit(receiver, toString(argRes)), nil
	case "join":
		sep := ""
		if len(node.Args) >= 2 {
			argRes, err := evalIn(node.Args[1], subject, env)
			if err != nil {
				return nil, err
			}
			sep = toString(argRes)
		}
		return []any{fnJoin(receiver, sep)}, nil
	case "toInteger":
		if len(receiver) == 0 {
			return nil, nil
		}
		if n, ok := toInteger(receiver[0]); ok {
			return []any{n}, nil
		}
		return nil, nil
	case "toDecimal":
		if len(receiver) == 0 {
			return nil, nil
		}
		if f, ok := toDecimal(receiver[0]); ok {
			return []any{f}, nil
		}
		return nil, nil
	case "toBoolean":
		if len(receiver) == 0 {
			return nil, nil
		}
		if b, ok := toBoolean(receiver[0]); ok {
			return []any{b}, nil
		}
		return nil, nil
	case "toDate":
		if len(receiver) == 0 {
			return nil, nil
		}
		if s, ok := toDate(receiver[0]); ok {
			return []any{s}, nil
		}
		return nil, nil
	case "toDateTime":
		if len(receiver) == 0 {
			return nil, nil
		}
		if s, ok := toDateTime(receiver[0]); ok {
			return []any{s}, nil
		}
		return nil, nil
	case "toTime":
		if len(receiver) == 0 {
			return nil, nil
		}
		if s, ok := toTime(receiver[0]); ok {
			return []any{s}, nil
		}
		return nil, nil
	case "convertsToInteger":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toInteger(receiver[0])
		return []any{ok}, nil
	case "convertsToDecimal":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toDecimal(receiver[0])
		return []any{ok}, nil
	case "convertsToString":
		// Per spec: anything non-empty converts (collections fail, but our
		// receiver is the post-flatten value list — single elements always
		// stringify cleanly).
		return []any{len(receiver) > 0}, nil
	case "convertsToBoolean":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toBoolean(receiver[0])
		return []any{ok}, nil
	case "convertsToDate":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toDate(receiver[0])
		return []any{ok}, nil
	case "convertsToDateTime":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toDateTime(receiver[0])
		return []any{ok}, nil
	case "convertsToTime":
		if len(receiver) == 0 {
			return []any{false}, nil
		}
		_, ok := toTime(receiver[0])
		return []any{ok}, nil
	case "now":
		return []any{nowISO()}, nil
	case "today":
		return []any{todayISO()}, nil
	}
	return nil, fmt.Errorf("unsupported function %q", node.Text)
}

// firstOf returns the first element of a collection, or nil for empty.
func firstOf(c []any) any {
	if len(c) == 0 {
		return nil
	}
	return c[0]
}

// stringify renders a single value as a string.
func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// evalEq returns the boolean result of the comparison.
func evalEq(node *Node, subject any, env map[string]any) ([]any, error) {
	left, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	right, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	if len(left) == 0 || len(right) == 0 {
		// FHIRPath: missing operand = empty result. We coerce to false so
		// `where(...)` filters predictably; this matches what mapping
		// authors expect from the official examples.
		return []any{false}, nil
	}
	if len(left) == 1 && len(right) == 1 {
		return []any{equalScalar(left[0], right[0])}, nil
	}
	// Bag-of-strings comparison for list = list.
	return []any{toString(left) == toString(right)}, nil
}

func evalAnd(node *Node, subject any, env map[string]any) ([]any, error) {
	l, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	if !truthy(l) {
		return []any{false}, nil
	}
	r, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	return []any{truthy(r)}, nil
}

func evalOr(node *Node, subject any, env map[string]any) ([]any, error) {
	l, err := evalIn(node.Args[0], subject, env)
	if err != nil {
		return nil, err
	}
	if truthy(l) {
		return []any{true}, nil
	}
	r, err := evalIn(node.Args[1], subject, env)
	if err != nil {
		return nil, err
	}
	return []any{truthy(r)}, nil
}

// truthy is FHIRPath's collection-to-boolean projection. Empty is false; a
// single boolean is its own value; anything else is true.
func truthy(c []any) bool {
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

func equalScalar(a, b any) bool {
	if a == b {
		return true
	}
	// json.Unmarshal yields float64 for numbers; we may compare against
	// int64 literals from the lexer. Normalize.
	if af, ok := numericFloat(a); ok {
		if bf, ok := numericFloat(b); ok {
			return af == bf
		}
	}
	// FHIR `code` and `string` interop: both end up as Go strings.
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			return as == bs
		}
	}
	return false
}

func numericFloat(v any) (float64, bool) {
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
	}
	return 0, false
}

// intArg converts a FHIRPath numeric argument to int with an explicit bound
// check. Numeric literals originate from strconv.ParseInt (int64); clamping to
// the 32-bit range (FHIR integers are 32-bit) keeps the conversion from
// overflowing a narrower int and is a no-op for any real index or count.
func intArg(f float64) int {
	if f >= math.MaxInt32 {
		return math.MaxInt32
	}
	if f <= math.MinInt32 {
		return math.MinInt32
	}
	return int(f)
}

func toString(c []any) string {
	if len(c) == 0 {
		return ""
	}
	var parts []string
	for _, v := range c {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, ",")
}
