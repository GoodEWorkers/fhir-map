package fhirpath

import "fmt"

// Node is the parsed AST shape. Each variant carries just the data needed
// for evaluation — there's no expression-rewriting step, so the AST stays
// minimal.
type Node struct {
	Kind       NodeKind
	Text       string
	IntValue   int64
	FloatValue float64
	BoolValue  bool
	Args       []*Node
}

// NodeKind enumerates the AST node types. Public so tests in other packages
// can inspect parse trees if needed.
type NodeKind int

const (
	nodeIdentifier NodeKind = iota
	nodeString
	nodeInteger
	nodeDecimal // decimal literal; FloatValue holds the value
	nodeBool
	nodePath       // chained field navigation: Args[0].Args[1]...
	nodeFunc       // .funcName(arg, ...): Text=funcName, Args[0]=receiver, Args[1..]=args
	nodeEq         // Args[0] = Args[1]
	nodeAnd        // Args[0] and Args[1]
	nodeOr         // Args[0] or Args[1]
	nodeNot        // not(Args[0])
	nodeExtConst   // M6.2 — %name external constant; Text = name (without leading %)
	nodeUnion      // M6.3 — (a | b | c); Args = operands
	nodeIn         // M6.3 — `a in b`; Args = [lhs, rhs]
	nodeContainsOp // M6.3 — `a contains b` (infix form, distinct from .contains())
	nodeCmp        // M6.8 — comparison: <, <=, >, >=, !=, ~, !~. Text carries the op.
	nodeArith      // M6.9 — arithmetic / concat: +, -, *, /, mod, div, &. Text carries the op.
	nodeIndex      // collection indexer expr[N]: Args[0]=receiver, IntValue=N (0-based)
	nodeComponent  // HL7v2 component/subcomponent after an index: Text=sep ('^'|'&'), IntValue=C (1-based)
)

// parser is a tiny recursive-descent parser. The grammar in BNF:
//
//	expr   := or
//	or     := and ('or' and)*
//	and    := eq  ('and' eq)*
//	eq     := unary ('=' unary)?
//	unary  := 'not' '(' expr ')' | primary
//	primary:= literal | ident path
//	path   := ('.' ident ('(' arglist? ')')?)*
//	ident  := identifier
//	arglist:= expr (',' expr)*
//	literal:= string | integer | decimal | bool
type parser struct {
	tokens []token
	pos    int
}

func (p *parser) peek() token { return p.tokens[p.pos] }
func (p *parser) peekAt(n int) token {
	if p.pos+n < len(p.tokens) {
		return p.tokens[p.pos+n]
	}
	return token{kind: tEOF}
}
func (p *parser) eat() token { t := p.tokens[p.pos]; p.pos++; return t }
func (p *parser) accept(k tokenKind) bool {
	if p.peek().kind == k {
		p.eat()
		return true
	}
	return false
}
func (p *parser) expect(k tokenKind, ctx string) (token, error) { //nolint:unparam // token return used by some callers
	t := p.eat()
	if t.kind != k {
		return token{}, fmt.Errorf("expected %s, got %q (%v) while parsing %s", tokenName(k), t.text, t.kind, ctx)
	}
	return t, nil
}

// parse runs the parser and returns the AST root.
func parse(src string) (*Node, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{tokens: toks}
	n, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, fmt.Errorf("trailing input after expression: %q", p.peek().text)
	}
	return n, nil
}

func (p *parser) parseExpr() (*Node, error) { return p.parseOr() }

func (p *parser) parseOr() (*Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tOr {
		p.eat()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &Node{Kind: nodeOr, Args: []*Node{left, right}}
	}
	return left, nil
}

func (p *parser) parseAnd() (*Node, error) {
	left, err := p.parseMembership()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tAnd {
		p.eat()
		right, err := p.parseMembership()
		if err != nil {
			return nil, err
		}
		left = &Node{Kind: nodeAnd, Args: []*Node{left, right}}
	}
	return left, nil
}

// parseMembership handles the infix `a in b` and `a contains b` forms
// (M6.3). `in` and `contains` both lex as plain tIdent so the lookahead
// matches on token text. Precedence sits between `and` (above) and `=`
// (below), matching the FHIRPath grammar's #membershipExpression slot.
//
// `.contains(arg)` (method form) is unaffected — it's consumed by
// parsePathTail before this rule fires.
func (p *parser) parseMembership() (*Node, error) {
	left, err := p.parseEq()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tIdent && (p.peek().text == "in" || p.peek().text == "contains") {
		op := p.eat().text
		right, err := p.parseEq()
		if err != nil {
			return nil, err
		}
		kind := nodeIn
		if op == "contains" {
			kind = nodeContainsOp
		}
		left = &Node{Kind: kind, Args: []*Node{left, right}}
	}
	return left, nil
}

func (p *parser) parseEq() (*Node, error) {
	left, err := p.parseCmp()
	if err != nil {
		return nil, err
	}
	switch p.peek().kind {
	case tEq:
		p.eat()
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		return &Node{Kind: nodeEq, Args: []*Node{left, right}}, nil
	case tNe, tEqv, tNeqv:
		op := p.eat().text
		right, err := p.parseCmp()
		if err != nil {
			return nil, err
		}
		return &Node{Kind: nodeCmp, Text: op, Args: []*Node{left, right}}, nil
	default:
		// non-operator token: not an equality op, leave for the caller
	}
	return left, nil
}

// parseCmp handles the inequality operators (<, <=, >, >=). Sits above
// parseEq so `a < b = true` parses as `(a < b) = true`. Mirrors the
// FHIRPath grammar's #inequalityExpression slot.
func (p *parser) parseCmp() (*Node, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tLt, tLe, tGt, tGe:
			op := p.eat().text
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = &Node{Kind: nodeCmp, Text: op, Args: []*Node{left, right}}
		default:
			return left, nil
		}
	}
}

// parseAdditive handles `+`, `-`, and `&` (string concat). Left-
// associative; sits between parseCmp and parseMultiplicative. M6.9.
func (p *parser) parseAdditive() (*Node, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek().kind {
		case tPlus, tMinus, tAmp:
			op := p.eat().text
			right, err := p.parseMultiplicative()
			if err != nil {
				return nil, err
			}
			left = &Node{Kind: nodeArith, Text: op, Args: []*Node{left, right}}
		default:
			return left, nil
		}
	}
}

// parseMultiplicative handles `*`, `/`, and the keyword forms `mod` and
// `div` (both lex as plain tIdent and are disambiguated by text). Left-
// associative; sits between parseAdditive and parseUnary. M6.9.
func (p *parser) parseMultiplicative() (*Node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.peek().kind == tStar || p.peek().kind == tSlash:
			op := p.eat().text
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &Node{Kind: nodeArith, Text: op, Args: []*Node{left, right}}
		case p.peek().kind == tIdent && (p.peek().text == "mod" || p.peek().text == "div"):
			op := p.eat().text
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &Node{Kind: nodeArith, Text: op, Args: []*Node{left, right}}
		default:
			return left, nil
		}
	}
}

func (p *parser) parseUnary() (*Node, error) {
	if p.peek().kind == tNot {
		p.eat()
		if _, err := p.expect(tLParen, "not("); err != nil {
			return nil, err
		}
		inner, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "not())"); err != nil {
			return nil, err
		}
		return &Node{Kind: nodeNot, Args: []*Node{inner}}, nil
	}
	// Unary polarity: model as `0 - x` / `0 + x` to reuse evalArith and preserve int64/float64 distinction, per FHIRPath spec.
	if k := p.peek().kind; k == tMinus || k == tPlus {
		op := p.eat().text
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		zero := &Node{Kind: nodeInteger, IntValue: 0, Text: "0"}
		return &Node{Kind: nodeArith, Text: op, Args: []*Node{zero, operand}}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (*Node, error) {
	switch p.peek().kind {
	case tString:
		t := p.eat()
		return &Node{Kind: nodeString, Text: t.text}, nil
	case tInteger:
		t := p.eat()
		return &Node{Kind: nodeInteger, IntValue: parseInt(t.text), Text: t.text}, nil
	case tDecimal:
		t := p.eat()
		return &Node{Kind: nodeDecimal, FloatValue: parseFloat(t.text), Text: t.text}, nil
	case tBool:
		t := p.eat()
		return &Node{Kind: nodeBool, BoolValue: t.text == "true", Text: t.text}, nil
	case tLParen:
		// Parenthesised sub-expression. M6.3: when the parens contain a
		// `|`-separated list, build a nodeUnion instead so `(a | b | c)`
		// produces a 3-element collection at eval time.
		p.eat()
		n, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind == tPipe {
			operands := []*Node{n}
			for p.peek().kind == tPipe {
				p.eat()
				next, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				operands = append(operands, next)
			}
			if _, err := p.expect(tRParen, "union literal"); err != nil {
				return nil, err
			}
			return p.parsePathTail(&Node{Kind: nodeUnion, Args: operands})
		}
		if _, err := p.expect(tRParen, "parenthesised expression"); err != nil {
			return nil, err
		}
		return p.parsePathTail(n)
	case tIdent:
		t := p.eat()
		// M6.8 — top-level function call form: `iif(a, b, c)`, `today()`.
		// The receiver becomes `$this` so the dispatcher in evalFunc has
		// a uniform shape; functions that don't care (iif, today, now)
		// just ignore it.
		if p.peek().kind == tLParen {
			p.eat()
			args := []*Node{{Kind: nodeIdentifier, Text: "$this"}}
			if p.peek().kind != tRParen {
				for {
					a, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if !p.accept(tComma) {
						break
					}
				}
			}
			if _, err := p.expect(tRParen, "function-call closing ')'"); err != nil {
				return nil, err
			}
			return p.parsePathTail(&Node{Kind: nodeFunc, Text: t.text, Args: args})
		}
		head := &Node{Kind: nodeIdentifier, Text: t.text}
		return p.parsePathTail(head)
	case tExtConst:
		// M6.2 — %name (or %'name'). The name carries no '%' on the token;
		// subsequent .path navigation chains via parsePathTail.
		t := p.eat()
		head := &Node{Kind: nodeExtConst, Text: t.text}
		return p.parsePathTail(head)
	default:
		// not a recognised primary token; fall through to error
	}
	return nil, fmt.Errorf("unexpected token %q", p.peek().text)
}

// parsePathTail consumes any chained `.ident` segments, including
// function-call form `.fn(arg, ...)`. Receives the head node already.
//
// M6.4 — `not` is a keyword token (so the prefix `not(x)` form is
// recognised) but it also valid as a method name `.not()` per FHIRPath.
// Accept tNot after a dot and treat it as the identifier "not".
func (p *parser) parsePathTail(head *Node) (*Node, error) {
	current := head
	for {
		// HL7v2 component (`[rep]-C`, split the field on '^') / subcomponent
		// (`[rep]-C-S`, split component C on '&'). Only consumed right after an
		// index or a prior component, so it never collides with `-` subtraction.
		if (current.Kind == nodeIndex || current.Kind == nodeComponent) &&
			p.peek().kind == tMinus && p.peekAt(1).kind == tInteger {
			p.eat() // '-'
			n := p.eat()
			sep := "^"
			if current.Kind == nodeComponent {
				sep = "&"
			}
			current = &Node{Kind: nodeComponent, Text: sep, IntValue: parseInt(n.text), Args: []*Node{current}}
			continue
		}
		// Collection indexer: `expr[N]` selects the N-th (0-based) item, e.g.
		// the HL7v2 path `PID-2[0]`. Chains with `.` navigation either side.
		if p.peek().kind == tLBracket {
			p.eat()
			idx, err := p.expect(tInteger, "index inside '[...]'")
			if err != nil {
				return nil, err
			}
			if _, err := p.expect(tRBracket, "closing ']' of indexer"); err != nil {
				return nil, err
			}
			current = &Node{Kind: nodeIndex, IntValue: parseInt(idx.text), Args: []*Node{current}}
			continue
		}
		if p.peek().kind != tDot {
			break
		}
		p.eat()
		var ident token
		switch p.peek().kind {
		case tIdent:
			ident = p.eat()
		case tNot:
			ident = token{text: "not"}
			p.eat()
		default:
			return nil, fmt.Errorf("expected identifier, got %q (%v) while parsing path segment after '.'",
				p.peek().text, p.peek().kind)
		}
		if p.peek().kind == tLParen {
			p.eat()
			var args []*Node
			args = append(args, current) // receiver as first arg
			if p.peek().kind != tRParen {
				for {
					a, err := p.parseExpr()
					if err != nil {
						return nil, err
					}
					args = append(args, a)
					if !p.accept(tComma) {
						break
					}
				}
			}
			if _, err := p.expect(tRParen, "function-call closing ')'"); err != nil {
				return nil, err
			}
			current = &Node{Kind: nodeFunc, Text: ident.text, Args: args}
			continue
		}
		// Plain field navigation: wrap into a path if not already one.
		if current.Kind == nodePath {
			current.Args = append(current.Args, &Node{Kind: nodeIdentifier, Text: ident.text})
		} else {
			current = &Node{
				Kind: nodePath,
				Args: []*Node{current, {Kind: nodeIdentifier, Text: ident.text}},
			}
		}
	}
	return current, nil
}

func tokenName(k tokenKind) string {
	switch k {
	case tIdent:
		return "identifier"
	case tInteger:
		return "integer"
	case tDecimal:
		return "decimal"
	case tString:
		return "string"
	case tBool:
		return "boolean"
	case tDot:
		return "'.'"
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tEq:
		return "'='"
	case tComma:
		return "','"
	case tAnd:
		return "'and'"
	case tOr:
		return "'or'"
	case tNot:
		return "'not'"
	case tExtConst:
		return "external constant"
	case tPipe:
		return "'|'"
	case tLt:
		return "'<'"
	case tLe:
		return "'<='"
	case tGt:
		return "'>'"
	case tGe:
		return "'>='"
	case tNe:
		return "'!='"
	case tEqv:
		return "'~'"
	case tNeqv:
		return "'!~'"
	case tPlus:
		return "'+'"
	case tMinus:
		return "'-'"
	case tStar:
		return "'*'"
	case tSlash:
		return "'/'"
	case tAmp:
		return "'&'"
	case tLBracket:
		return "'['"
	case tRBracket:
		return "']'"
	case tEOF:
		return "end of input"
	}
	return "unknown"
}
