package fml

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// Parse tokenises and parses FML text into a StructureMap. Returns a typed
// error pointing at the first failing token if the input is malformed.
func Parse(src string) (*structuremap.StructureMap, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	return p.parseUnit()
}

type parser struct {
	toks []tok
	pos  int
}

func (p *parser) peek() tok { return p.toks[p.pos] }
func (p *parser) eat() tok  { t := p.toks[p.pos]; p.pos++; return t }
func (p *parser) accept(k tokKind) bool {
	if p.peek().kind == k {
		p.eat()
		return true
	}
	return false
}
func (p *parser) expect(k tokKind, ctx string) (tok, error) {
	t := p.eat()
	if t.kind != k {
		return tok{}, fmt.Errorf("line %d: expected %s, got %q while parsing %s", t.line, kindName(k), t.text, ctx)
	}
	return t, nil
}

// parseUnit parses the FML top-level declaration, groups, and inline concept maps.
func (p *parser) parseUnit() (*structuremap.StructureMap, error) {
	sm := &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Status:       "active",
	}
	for p.peek().kind == tDocComment {
		body := p.eat().text
		if eqIdx := strings.Index(body, "="); eqIdx >= 0 {
			key := strings.TrimSpace(body[:eqIdx])
			val := strings.TrimSpace(body[eqIdx+1:])
			val = strings.Trim(val, "\"'")
			switch key {
			case "url":
				sm.URL = val
			case "name":
				sm.Name = val
			}
		}
	}
	if p.peek().kind == tKwMap {
		p.eat() // consume 'map'
		urlTok, err := p.expect(tString, "map URL")
		if err != nil {
			return nil, err
		}
		if _, err2 := p.expect(tEq, "map declaration '='"); err2 != nil {
			return nil, err2
		}
		nameTok, err := p.expect(tString, "map name")
		if err != nil {
			return nil, err
		}
		sm.URL = urlTok.text
		sm.Name = nameTok.text
	} else if sm.URL == "" {
		return nil, fmt.Errorf("line %d: expected 'map' declaration or '///' metadata header", p.peek().line)
	}

	var pendingDoc string
	for p.peek().kind != tEOF {
		switch p.peek().kind {
		case tDocComment:
			pendingDoc = p.eat().text
		case tKwUses:
			s, err := p.parseUses()
			if err != nil {
				return nil, err
			}
			sm.Structure = append(sm.Structure, s)
		case tKwImports:
			p.eat()
			urlT, err := p.expect(tString, "imports URL")
			if err != nil {
				return nil, err
			}
			sm.Import = append(sm.Import, urlT.text)
		case tKwGroup:
			g, err := p.parseGroup()
			if err != nil {
				return nil, err
			}
			if pendingDoc != "" {
				g.Documentation = pendingDoc
				pendingDoc = ""
			}
			sm.Group = append(sm.Group, g)
		case tKwConceptMap:
			cm, err := p.parseConceptMap()
			if err != nil {
				return nil, err
			}
			sm.Contained = append(sm.Contained, cm)
		case tKwLet:
			r, err := p.parseLet()
			if err != nil {
				return nil, err
			}
			sm.Const = append(sm.Const, r)
		default:
			return nil, fmt.Errorf("line %d: unexpected token %q at top level", p.peek().line, p.peek().text)
		}
	}
	if len(sm.Group) == 0 {
		return nil, fmt.Errorf("FML must contain at least one group")
	}
	return sm, nil
}

// parseUses consumes a `uses "url" alias name as mode` declaration.
func (p *parser) parseUses() (structuremap.Structure, error) {
	p.eat() // consume 'uses'
	urlT, err := p.expect(tString, "uses URL")
	if err != nil {
		return structuremap.Structure{}, err
	}
	s := structuremap.Structure{URL: urlT.text}
	if p.accept(tKwAlias) {
		aliasT, err := p.expect(tIdent, "uses alias name")
		if err != nil {
			return s, err
		}
		s.Alias = aliasT.text
	}
	if p.accept(tKwAs) {
		// source/target are keywords but allowed as mode words here; parse as identifiers.
		t := p.peek()
		switch t.kind {
		case tIdent, tKwSource, tKwTarget:
			p.eat()
			s.Mode = t.text
		default:
			return s, fmt.Errorf("line %d: expected %s, got %q while parsing uses mode", t.line, kindName(tIdent), t.text)
		}
	}
	return s, nil
}

// parseGroup consumes a group declaration: `group name(params) { rules }`.
// Optional `extends parent` clause and `<<type+types>>` directive are
// allowed but not required.
func (p *parser) parseGroup() (structuremap.Group, error) {
	p.eat() // consume 'group'
	nameT, err := p.expect(tIdent, "group name")
	if err != nil {
		return structuremap.Group{}, err
	}
	g := structuremap.Group{Name: nameT.text}

	if _, err := p.expect(tLParen, "group parameter list '('"); err != nil {
		return g, err
	}
	if p.peek().kind != tRParen {
		for {
			in, err := p.parseInput()
			if err != nil {
				return g, err
			}
			g.Input = append(g.Input, in)
			if !p.accept(tComma) {
				break
			}
		}
	}
	if _, err := p.expect(tRParen, "group parameter list ')'"); err != nil {
		return g, err
	}

	if p.accept(tKwExtends) {
		parentT, err := p.expect(tIdent, "extends parent name")
		if err != nil {
			return g, err
		}
		g.Extends = parentT.text
	}

	if p.accept(tLAngleAngle) {
		t := p.peek()
		switch {
		case t.kind == tKwTypes:
			p.eat()
			g.TypeMode = "types"
		case t.kind == tKwTypeAndTypes:
			p.eat()
			g.TypeMode = "type+types"
		case t.kind == tIdent && t.text == "type" &&
			p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tPlus &&
			p.pos+2 < len(p.toks) && p.toks[p.pos+2].kind == tKwTypes:
			p.pos += 3 // consume `type` `+` `types`
			g.TypeMode = "type+types"
		case t.kind == tIdent && t.text == "type" &&
			p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tPlus &&
			(p.pos+2 >= len(p.toks) || p.toks[p.pos+2].kind == tRAngleAngle):
			p.pos += 2
			g.TypeMode = "type+"
		default:
			return g, fmt.Errorf("line %d: expected `types` or `type+types` inside `<<...>>`, got %q", t.line, t.text)
		}
		if _, err := p.expect(tRAngleAngle, "group type-mode '>>'"); err != nil {
			return g, err
		}
	}

	if _, err := p.expect(tLBrace, "group body '{'"); err != nil {
		return g, err
	}
	if err := p.parseGroupBody(&g); err != nil {
		return g, err
	}
	if _, err := p.expect(tRBrace, "group body '}'"); err != nil {
		return g, err
	}
	return g, nil
}

// parseGroupBody consumes statements inside `{ ... }`. Statements are
// rules, `let` bindings, and `///` doc comments that attach to the next
// rule. Used by both top-level groups and `then { ... }` anonymous
// nested blocks.
func (p *parser) parseGroupBody(g *structuremap.Group) error {
	var pendingDoc string
	for p.peek().kind != tRBrace && p.peek().kind != tEOF {
		switch p.peek().kind {
		case tDocComment:
			pendingDoc = p.eat().text
		case tKwLet:
			r, err := p.parseLet()
			if err != nil {
				return err
			}
			if pendingDoc != "" {
				r.Documentation = pendingDoc
				pendingDoc = ""
			}
			g.Rule = append(g.Rule, r)
		default:
			r, err := p.parseRule()
			if err != nil {
				return err
			}
			if pendingDoc != "" {
				r.Documentation = pendingDoc
				pendingDoc = ""
			}
			g.Rule = append(g.Rule, r)
		}
	}
	return nil
}

// parseLet consumes `let IDENT = expr ;` and emits a synthetic Rule
// carrying a single Source whose Variable is the bound name. The
// expression's head identifier lands on Source.Context and the dotted
// tail (if any) on Source.Element. For non-path expressions (operators,
// function calls), the entire token run is captured into Source.Element
// as raw text — the executor can evaluate it as a FHIRPath expression
// against the rule's scope.
func (p *parser) parseLet() (structuremap.Rule, error) {
	p.eat() // consume 'let'
	nameT, err := p.expect(tIdent, "let binding name")
	if err != nil {
		return structuremap.Rule{}, err
	}
	if _, err := p.expect(tEq, "let '='"); err != nil {
		return structuremap.Rule{}, err
	}
	var (
		pathOK      = true
		pathHead    string
		pathTail    strings.Builder
		raw         strings.Builder
		expectIdent = true
	)
	for p.peek().kind != tSemi && p.peek().kind != tEOF {
		t := p.eat()
		if raw.Len() > 0 {
			raw.WriteByte(' ')
		}
		switch t.kind {
		case tString:
			raw.WriteByte('\'')
			raw.WriteString(t.text)
			raw.WriteByte('\'')
			pathOK = false
		default:
			raw.WriteString(t.text)
		}
		if !pathOK {
			continue
		}
		switch {
		case expectIdent && t.kind == tIdent:
			if pathHead == "" {
				pathHead = t.text
			} else {
				if pathTail.Len() > 0 {
					pathTail.WriteByte('.')
				}
				pathTail.WriteString(t.text)
			}
			expectIdent = false
		case !expectIdent && t.kind == tDot:
			expectIdent = true
		default:
			pathOK = false
		}
	}
	if _, err := p.expect(tSemi, "let terminator ';'"); err != nil {
		return structuremap.Rule{}, err
	}
	s := structuremap.Source{Variable: nameT.text}
	if pathOK && pathHead != "" {
		s.Context = pathHead
		s.Element = pathTail.String()
	} else {
		s.Element = strings.TrimSpace(raw.String())
	}
	return structuremap.Rule{Source: []structuremap.Source{s}}, nil
}

// parseInput consumes a group input declaration:
//
//	[source|target] name [: Type]
func (p *parser) parseInput() (structuremap.Input, error) {
	var in structuremap.Input
	switch p.peek().kind {
	case tKwSource:
		p.eat()
		in.Mode = "source"
	case tKwTarget:
		p.eat()
		in.Mode = "target"
	default:
		// no mode keyword present; mode will be empty string
	}
	nameT, err := p.expect(tIdent, "input name")
	if err != nil {
		return in, err
	}
	in.Name = nameT.text
	if p.accept(tColon) {
		typeT, err := p.expect(tIdent, "input type")
		if err != nil {
			return in, err
		}
		in.Type = typeT.text
	}
	return in, nil
}

// parseRule consumes one rule. Form:
//
//	sourceList -> targetList [name] ;
//
// Where sourceList = source (',' source)* and similarly for target.
// The label after the targets is the rule's `name` per FML convention.
func (p *parser) parseRule() (structuremap.Rule, error) {
	var rule structuremap.Rule

	for {
		src, err := p.parseSource()
		if err != nil {
			return rule, err
		}
		rule.Source = append(rule.Source, src)
		if !p.accept(tComma) {
			break
		}
	}

	if p.peek().kind == tArrow {
		p.eat()
		for {
			tgt, err := p.parseTarget()
			if err != nil {
				return rule, err
			}
			rule.Target = append(rule.Target, tgt)
			if !p.accept(tComma) {
				break
			}
		}
	}

	if p.accept(tKwThen) {
		switch p.peek().kind {
		case tLBrace:
			p.eat() // consume '{'
			var sub structuremap.Group
			if err := p.parseGroupBody(&sub); err != nil {
				return rule, err
			}
			if _, err := p.expect(tRBrace, "then block '}'"); err != nil {
				return rule, err
			}
			rule.Rule = append(rule.Rule, sub.Rule...)
		case tKwMap:
			for {
				dep, err := p.parseThenMapGroupCall()
				if err != nil {
					return rule, err
				}
				rule.Dependent = append(rule.Dependent, dep)
				if !p.accept(tComma) {
					break
				}
			}
		case tIdent:
			for {
				dep, err := p.parseDependentCall()
				if err != nil {
					return rule, err
				}
				rule.Dependent = append(rule.Dependent, dep)
				if !p.accept(tComma) {
					break
				}
			}
		default:
			return rule, fmt.Errorf("line %d: expected `{`, `map`, or dependent-group name after `then`, got %q", p.peek().line, p.peek().text)
		}
	}

	if p.peek().kind == tString {
		rule.Name = p.eat().text
	}

	if _, err := p.expect(tSemi, "rule terminator ';'"); err != nil {
		return rule, err
	}
	return rule, nil
}

// parseThenMapGroupCall parses a canonical-URL form dependent group call.
func (p *parser) parseThenMapGroupCall() (structuremap.Dependent, error) {
	var dep structuremap.Dependent
	if _, err := p.expect(tKwMap, "then 'map' keyword"); err != nil {
		return dep, err
	}
	urlT, err := p.expect(tString, "then map URL")
	if err != nil {
		return dep, err
	}
	dep.MapURL = urlT.text
	if _, err2 := p.expect(tKwGroup, "then map 'group' keyword"); err2 != nil {
		return dep, err2
	}
	nameT, err := p.expect(tIdent, "then map group name")
	if err != nil {
		return dep, err
	}
	dep.Name = nameT.text
	if _, err := p.expect(tLParen, "then map group call '('"); err != nil {
		return dep, err
	}
	if p.peek().kind != tRParen {
		for {
			arg, err := p.parseTransformArg()
			if err != nil {
				return dep, err
			}
			dep.Parameter = append(dep.Parameter, arg)
			if !p.accept(tComma) {
				break
			}
		}
	}
	if _, err := p.expect(tRParen, "then map group call ')'"); err != nil {
		return dep, err
	}
	return dep, nil
}

// parseDependentCall parses a dependent group call (then Name(args)).
func (p *parser) parseDependentCall() (structuremap.Dependent, error) {
	var dep structuremap.Dependent
	nameT, err := p.expect(tIdent, "dependent group name")
	if err != nil {
		return dep, err
	}
	dep.Name = nameT.text
	if _, err := p.expect(tLParen, "dependent call '('"); err != nil {
		return dep, err
	}
	if p.peek().kind != tRParen {
		for {
			arg, err := p.parseTransformArg()
			if err != nil {
				return dep, err
			}
			dep.Parameter = append(dep.Parameter, arg)
			if !p.accept(tComma) {
				break
			}
		}
	}
	if _, err := p.expect(tRParen, "dependent call ')'"); err != nil {
		return dep, err
	}
	return dep, nil
}

// parseSource consumes a source binding:
//
//	context[.element] [where '(' fhirpath ')'] [as variable]
func (p *parser) parseSource() (structuremap.Source, error) {
	var s structuremap.Source
	ctxT, err := p.expect(tIdent, "source context")
	if err != nil {
		return s, err
	}
	s.Context = ctxT.text
	if p.accept(tDot) {
		elem, err := p.parseDottedIdent()
		if err != nil {
			return s, err
		}
		s.Element = elem
	}
	if p.accept(tColon) {
		typeT, err := p.expect(tIdent, "source element type")
		if err != nil {
			return s, err
		}
		s.Type = typeT.text
	}
	if p.peek().kind == tCardinality {
		card := p.eat().text
		idx := strings.Index(card, "..")
		if idx < 0 {
			return s, fmt.Errorf("line %d: malformed cardinality token %q (missing '..')", p.peek().line, card)
		}
		minStr := card[:idx]
		maxStr := card[idx+2:]
		var n int
		if _, err := fmt.Sscanf(minStr, "%d", &n); err != nil {
			return s, fmt.Errorf("line %d: invalid sourceCardinality min %q", p.peek().line, minStr)
		}
		s.Min = &n
		s.Max = maxStr
	}
	seenAs, seenWhere := false, false
	for {
		switch p.peek().kind {
		case tKwWhere:
			if seenWhere {
				return s, fmt.Errorf("line %d: duplicate `where` clause on source", p.peek().line)
			}
			seenWhere = true
			p.eat()
			if p.peek().kind == tLParen {
				p.eat()
				expr, err := p.captureParenBalanced()
				if err != nil {
					return s, err
				}
				s.Condition = strings.TrimSpace(expr)
			} else {
				s.Condition = strings.TrimSpace(p.captureUntilRuleClause())
			}
		case tKwAs:
			if seenAs {
				return s, fmt.Errorf("line %d: duplicate `as` clause on source", p.peek().line)
			}
			seenAs = true
			p.eat()
			varT, err := p.expect(tIdent, "source variable")
			if err != nil {
				return s, err
			}
			s.Variable = varT.text
		case tKwLog:
			p.eat()
			if _, err := p.expect(tLParen, "log '('"); err != nil {
				return s, err
			}
			logExpr, err := p.captureParenBalanced()
			if err != nil {
				return s, err
			}
			s.LogMessage = strings.TrimSpace(logExpr)
		case tKwCheck:
			if s.Check != "" {
				return s, fmt.Errorf("line %d: duplicate `check` clause on source", p.peek().line)
			}
			p.eat()
			if _, err := p.expect(tLParen, "check '('"); err != nil {
				return s, err
			}
			expr, err := p.captureParenBalanced()
			if err != nil {
				return s, err
			}
			s.Check = strings.TrimSpace(expr)
		case tIdent:
			switch word := p.peek().text; word {
			case "default":
				if len(s.DefaultValue) > 0 {
					return s, fmt.Errorf("line %d: duplicate `default` clause on source", p.peek().line)
				}
				p.eat()
				raw, typ, err := p.parseSourceDefault()
				if err != nil {
					return s, err
				}
				s.DefaultValue, s.DefaultValueType = raw, typ
			default:
				mode, ok := sourceListMode(word)
				if !ok {
					return s, nil
				}
				if s.ListMode != "" {
					return s, fmt.Errorf("line %d: duplicate source list mode", p.peek().line)
				}
				p.eat()
				s.ListMode = mode
			}
		default:
			return s, nil
		}
	}
}

// parseTarget consumes a target action:
//
//	context[.element] [= transform(args)] [as variable]
//
// When the transform is omitted entirely (implicit copy), the assignment
// uses the (single) source variable bound on the rule.
func (p *parser) parseTarget() (structuremap.Target, error) {
	var t structuremap.Target
	ctxT, err := p.expect(tIdent, "target context")
	if err != nil {
		return t, err
	}
	t.Context = ctxT.text
	if p.accept(tDot) {
		elem, err := p.parseDottedIdent()
		if err != nil {
			return t, err
		}
		t.Element = elem
	}
	if p.accept(tEq) {
		if p.peek().kind == tLParen {
			p.eat() // consume '('
			expr, err := p.captureParenBalanced()
			if err != nil {
				return t, err
			}
			t.Transform = "evaluate"
			t.Parameter = []structuremap.Parameter{{ValueString: strings.TrimSpace(expr)}}
			if p.accept(tKwAs) {
				varT, err := p.expect(tIdent, "target variable")
				if err != nil {
					return t, err
				}
				t.Variable = varT.text
			}
			return t, nil
		}
		if p.peek().kind == tString {
			strT := p.eat()
			t.Transform = "copy"
			t.Parameter = []structuremap.Parameter{{ValueString: strT.text}}
			if p.accept(tKwAs) {
				varT, err := p.expect(tIdent, "target variable")
				if err != nil {
					return t, err
				}
				t.Variable = varT.text
			}
			return t, nil
		}
		nameT, err := p.expect(tIdent, "target transform name")
		if err != nil {
			return t, err
		}
		if p.peek().kind == tLParen {
			t.Transform = nameT.text
			p.eat()
			if t.Transform == "evaluate" {
				ctxArg, err := p.parseTransformArg()
				if err != nil {
					return t, err
				}
				t.Parameter = append(t.Parameter, ctxArg)
				if _, cerr := p.expect(tComma, "evaluate(context, expr) ','"); cerr != nil {
					return t, cerr
				}
				var exprStr string
				if p.peek().kind == tString && p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tRParen {
					exprStr = p.eat().text // single quoted string: its content is the expression
					if _, rerr := p.expect(tRParen, "evaluate(...) ')'"); rerr != nil {
						return t, rerr
					}
				} else {
					expr, cerr := p.captureParenBalanced() // captures the expression and consumes ')'
					if cerr != nil {
						return t, cerr
					}
					exprStr = strings.TrimSpace(expr)
				}
				t.Parameter = append(t.Parameter, structuremap.Parameter{ValueString: exprStr})
			} else {
				if p.peek().kind != tRParen {
					for {
						arg, err := p.parseTransformArg()
						if err != nil {
							return t, err
						}
						t.Parameter = append(t.Parameter, arg)
						if !p.accept(tComma) {
							break
						}
					}
				}
				if _, err := p.expect(tRParen, "transform args ')'"); err != nil {
					return t, err
				}
			}
		} else {
			t.Transform = "copy"
			t.Parameter = []structuremap.Parameter{{ValueID: nameT.text}}
		}
	}
	if p.accept(tKwAs) {
		varT, err := p.expect(tIdent, "target variable")
		if err != nil {
			return t, err
		}
		t.Variable = varT.text
	}
	for p.peek().kind == tIdent {
		switch mode := p.peek().text; mode {
		case "first", "last", "single", "collate":
			p.eat()
			t.ListMode = append(t.ListMode, mode)
		case "share":
			p.eat()
			ruleIDT, err := p.expect(tIdent, "share list rule id")
			if err != nil {
				return t, err
			}
			t.ListMode = append(t.ListMode, "share")
			t.ListRuleId = ruleIDT.text
		default:
			return t, nil
		}
	}
	return t, nil
}

// parseTransformArg parses a single argument to a transform invocation.
func (p *parser) parseTransformArg() (structuremap.Parameter, error) {
	switch p.peek().kind {
	case tString:
		return structuremap.Parameter{ValueString: p.eat().text}, nil
	case tMinus, tPlus:
		// A signed numeric literal: `copy(-5)` / `copy(-1.5)` / `copy(+7)`.
		// FHIRPath treats the sign as a unary operator on the literal; for a
		// direct argument we fold it into the literal's value. Anything other
		// than a number after the sign is not a valid argument.
		neg := p.eat().kind == tMinus
		if p.peek().kind != tNumber {
			return structuremap.Parameter{}, fmt.Errorf("line %d: expected a number after %q in transform argument", p.peek().line, map[bool]string{true: "-", false: "+"}[neg])
		}
		return p.numericArg(p.eat().text, neg)
	case tNumber:
		return p.numericArg(p.eat().text, false)
	case tIdent:
		head := p.eat().text
		var sb strings.Builder
		sb.WriteString(head)
		for p.peek().kind == tDot {
			p.eat()
			next, err := p.expect(tIdent, "transform-arg path segment")
			if err != nil {
				return structuremap.Parameter{}, err
			}
			sb.WriteString(".")
			sb.WriteString(next.text)
		}
		return structuremap.Parameter{ValueID: sb.String()}, nil
	default:
	}
	return structuremap.Parameter{}, fmt.Errorf("line %d: unexpected transform argument %q", p.peek().line, p.peek().text)
}

// sourceListMode maps FML source list-mode words to FHIR R5 wire codes.
func sourceListMode(word string) (string, bool) {
	switch word {
	case "first":
		return "first", true
	case "last":
		return "last", true
	case "not_first":
		return "not-first", true
	case "not_last":
		return "not-last", true
	case "only_one":
		return "only-one", true
	}
	return "", false
}

// parseSourceDefault parses a default value and returns it as JSON plus its type name.
func (p *parser) parseSourceDefault() (json.RawMessage, string, error) {
	if _, err := p.expect(tLParen, "default '('"); err != nil {
		return nil, "", err
	}
	neg := false
	if k := p.peek().kind; k == tMinus || k == tPlus {
		neg = p.eat().kind == tMinus
	}
	lit := p.eat()
	var raw json.RawMessage
	var typ string
	switch {
	case lit.kind == tString:
		b, err := json.Marshal(lit.text)
		if err != nil {
			return nil, "", err
		}
		raw, typ = b, "string"
	case lit.kind == tNumber:
		num := lit.text
		if neg {
			num = "-" + num
		}
		raw = json.RawMessage(num) // a numeric literal is already valid JSON
		if strings.Contains(num, ".") {
			typ = "decimal"
		} else {
			typ = "integer"
		}
	case lit.kind == tIdent && (lit.text == "true" || lit.text == "false"):
		raw, typ = json.RawMessage(lit.text), "boolean"
	default:
		return nil, "", fmt.Errorf("line %d: unsupported default value %q (expected string, number, or boolean)", lit.line, lit.text)
	}
	if _, err := p.expect(tRParen, "default ')'"); err != nil {
		return nil, "", err
	}
	return raw, typ, nil
}

// numericArg builds a numeric Parameter, choosing integer or decimal based on presence of '.'.
func (p *parser) numericArg(text string, neg bool) (structuremap.Parameter, error) {
	if strings.Contains(text, ".") {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return structuremap.Parameter{}, err
		}
		if neg {
			f = -f
		}
		return structuremap.Parameter{ValueDecimal: &f}, nil
	}
	var n int
	if _, err := fmt.Sscanf(text, "%d", &n); err != nil {
		return structuremap.Parameter{}, err
	}
	if neg {
		n = -n
	}
	return structuremap.Parameter{ValueInteger: &n}, nil
}

// parseDottedIdent parses a dotted path (source/target element).
func (p *parser) parseDottedIdent() (string, error) {
	first, err := p.expect(tIdent, "dotted-path segment")
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString(first.text)
	for p.peek().kind == tDot {
		p.eat()
		next, err := p.expect(tIdent, "dotted-path segment")
		if err != nil {
			return "", err
		}
		sb.WriteString(".")
		sb.WriteString(next.text)
	}
	return sb.String(), nil
}

// captureParenBalanced reads tokens until the matching ')' with proper spacing.
func (p *parser) captureParenBalanced() (string, error) {
	depth := 1
	var sb strings.Builder
	prev := tEOF // sentinel: no previous token yet
	for {
		t := p.peek()
		if t.kind == tEOF {
			return "", fmt.Errorf("unterminated where(...) clause")
		}
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			depth--
			if depth == 0 {
				p.eat() // consume the matching ')'
				return sb.String(), nil
			}
		default:
			// other tokens are accumulated as-is
		}
		p.eat()
		if sb.Len() > 0 && needsSpace(prev, t.kind) {
			sb.WriteByte(' ')
		}
		// Reconstruct text. For strings, preserve quotes so the FHIRPath
		// evaluator parses them as literals.
		switch t.kind {
		case tString:
			sb.WriteByte('\'')
			sb.WriteString(t.text)
			sb.WriteByte('\'')
		default:
			sb.WriteString(t.text)
		}
		prev = t.kind
	}
}

// captureUntilRuleClause reads an unparenthesised FHIRPath expression until a rule boundary.
func (p *parser) captureUntilRuleClause() string {
	var sb strings.Builder
	prev := tEOF
	depth := 0
	for {
		t := p.peek()
		if t.kind == tEOF {
			break
		}
		if depth == 0 {
			switch t.kind {
			case tArrow, tKwThen, tKwAs, tKwWhere, tKwLog, tSemi, tComma, tRBrace:
				return sb.String()
			default:
			}
		}
		p.eat()
		switch t.kind {
		case tLParen:
			depth++
		case tRParen:
			depth--
		default:
		}
		if sb.Len() > 0 && needsSpace(prev, t.kind) {
			sb.WriteByte(' ')
		}
		switch t.kind {
		case tString:
			sb.WriteByte('\'')
			sb.WriteString(t.text)
			sb.WriteByte('\'')
		default:
			sb.WriteString(t.text)
		}
		prev = t.kind
	}
	return sb.String()
}

// needsSpace reports whether two adjacent tokens in a captured FHIRPath
// expression need whitespace between them. Punctuation that binds to its
// neighbour (`.`, `,`, paren boundaries) never gets a space; other token
// pairs do, so `linkId = 'first'` stays separated.
func needsSpace(prev, next tokKind) bool {
	if prev == tDot || next == tDot {
		return false
	}
	if prev == tLParen || next == tRParen {
		return false
	}
	if next == tComma {
		return false
	}
	return true
}

// parseConceptMap parses an inline ConceptMap declaration and stores it on StructureMap.Contained.
func (p *parser) parseConceptMap() (*conceptmap.ConceptMap, error) {
	p.eat() // consume 'conceptmap'
	urlT, err := p.expect(tString, "conceptmap URL")
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tLBrace, "conceptmap body '{'"); err != nil {
		return nil, err
	}

	cm := &conceptmap.ConceptMap{
		ResourceType: "ConceptMap",
		URL:          urlT.text,
		Status:       "active",
	}
	if strings.HasPrefix(urlT.text, "#") {
		cm.ID = strings.TrimPrefix(urlT.text, "#")
	} else if !strings.Contains(urlT.text, "://") {
		cm.ID = urlT.text
	}

	prefixes := map[string]string{}
	var rows []cmRow

	for p.peek().kind != tRBrace && p.peek().kind != tEOF {
		switch p.peek().kind {
		case tKwPrefix:
			p.eat()
			nameT, err := p.expect(tIdent, "prefix name")
			if err != nil {
				return nil, err
			}
			if _, err2 := p.expect(tEq, "prefix '='"); err2 != nil {
				return nil, err2
			}
			uriT, err := p.expect(tString, "prefix URI")
			if err != nil {
				return nil, err
			}
			prefixes[nameT.text] = uriT.text
		case tIdent:
			r, err := p.parseConceptMapRow(prefixes)
			if err != nil {
				return nil, err
			}
			rows = append(rows, r)
		default:
			return nil, fmt.Errorf("line %d: unexpected token %q inside conceptmap body", p.peek().line, p.peek().text)
		}
	}
	if _, err := p.expect(tRBrace, "conceptmap body '}'"); err != nil {
		return nil, err
	}

	type gkey struct{ src, tgt string }
	groupIdx := map[gkey]int{}
	for _, r := range rows {
		gk := gkey{r.srcSystem, r.tgtSystem}
		gi, ok := groupIdx[gk]
		if !ok {
			cm.Group = append(cm.Group, conceptmap.Group{
				Source: r.srcSystem,
				Target: r.tgtSystem,
			})
			gi = len(cm.Group) - 1
			groupIdx[gk] = gi
		}
		g := &cm.Group[gi]
		var elem *conceptmap.Element
		for i := range g.Element {
			if g.Element[i].Code == r.srcCode {
				elem = &g.Element[i]
				break
			}
		}
		if elem == nil {
			g.Element = append(g.Element, conceptmap.Element{Code: r.srcCode})
			elem = &g.Element[len(g.Element)-1]
		}
		elem.Target = append(elem.Target, conceptmap.Target{
			Code:         r.tgtCode,
			Relationship: r.rel,
		})
	}
	return cm, nil
}

type cmRow struct {
	srcSystem string
	srcCode   string
	tgtSystem string
	tgtCode   string
	rel       string
}

// parseConceptMapRow parses a single concept map row.
func (p *parser) parseConceptMapRow(prefixes map[string]string) (cmRow, error) {
	var r cmRow
	srcPrefix, err := p.expect(tIdent, "conceptmap row source prefix")
	if err != nil {
		return r, err
	}
	srcSystem, ok := prefixes[srcPrefix.text]
	if !ok {
		return r, fmt.Errorf("line %d: undeclared prefix %q in conceptmap row", srcPrefix.line, srcPrefix.text)
	}
	if _, err2 := p.expect(tColon, "conceptmap row ':'"); err2 != nil {
		return r, err2
	}
	srcCode, err := p.parseConceptMapCode()
	if err != nil {
		return r, err
	}

	rel, err := p.parseRelationshipOp()
	if err != nil {
		return r, err
	}

	tgtPrefix, err := p.expect(tIdent, "conceptmap row target prefix")
	if err != nil {
		return r, err
	}
	tgtSystem, ok := prefixes[tgtPrefix.text]
	if !ok {
		return r, fmt.Errorf("line %d: undeclared prefix %q in conceptmap row", tgtPrefix.line, tgtPrefix.text)
	}
	if _, err2 := p.expect(tColon, "conceptmap row ':'"); err2 != nil {
		return r, err2
	}
	tgtCode, err := p.parseConceptMapCode()
	if err != nil {
		return r, err
	}

	r.srcSystem = srcSystem
	r.srcCode = srcCode
	r.tgtSystem = tgtSystem
	r.tgtCode = tgtCode
	r.rel = rel
	return r, nil
}

// parseConceptMapCode parses a concept map code (quoted string or identifier).
func (p *parser) parseConceptMapCode() (string, error) {
	switch p.peek().kind {
	case tString:
		return p.eat().text, nil
	case tIdent:
		return p.eat().text, nil
	default:
	}
	return "", fmt.Errorf("line %d: expected code (identifier or quoted string), got %q", p.peek().line, p.peek().text)
}

// parseRelationshipOp parses a concept map relationship operator.
func (p *parser) parseRelationshipOp() (string, error) {
	t := p.eat()
	switch t.kind {
	case tMinus:
		return "related-to", nil
	case tEqEq:
		return "equivalent", nil
	case tBangEq:
		return "not-related-to", nil
	default:
	}
	return "", fmt.Errorf("line %d: expected relationship operator (-, ==, !=), got %q", t.line, t.text)
}

func kindName(k tokKind) string {
	switch k {
	case tIdent:
		return "identifier"
	case tNumber:
		return "number"
	case tString:
		return "string"
	case tLBrace:
		return "'{'"
	case tRBrace:
		return "'}'"
	case tLParen:
		return "'('"
	case tRParen:
		return "')'"
	case tColon:
		return "':'"
	case tComma:
		return "','"
	case tSemi:
		return "';'"
	case tDot:
		return "'.'"
	case tEq:
		return "'='"
	case tArrow:
		return "'->'"
	case tEOF:
		return "end of input"
	case tKwMap:
		return "'map'"
	case tKwUses:
		return "'uses'"
	case tKwImports:
		return "'imports'"
	case tKwGroup:
		return "'group'"
	case tKwExtends:
		return "'extends'"
	case tKwSource:
		return "'source'"
	case tKwTarget:
		return "'target'"
	case tKwAs:
		return "'as'"
	case tKwWhere:
		return "'where'"
	case tKwThen:
		return "'then'"
	case tKwLet:
		return "'let'"
	case tCardinality:
		return "cardinality"
	case tLAngleAngle:
		return "'<<'"
	case tRAngleAngle:
		return "'>>'"
	case tLt:
		return "'<'"
	case tLe:
		return "'<='"
	case tGt:
		return "'>'"
	case tGe:
		return "'>='"
	case tDocComment:
		return "doc-comment"
	case tEqEq:
		return "'=='"
	case tKwConceptMap:
		return "'conceptmap'"
	case tKwPrefix:
		return "'prefix'"
	case tBangEq:
		return "'!='"
	case tPlus:
		return "'+'"
	case tMinus:
		return "'-'"
	case tStar:
		return "'*'"
	case tSlash:
		return "'/'"
	case tKwCheck:
		return "'check'"
	case tKwLog:
		return "'log'"
	case tKwTypes:
		return "'types'"
	case tKwTypeAndTypes:
		return "'type+types'"
	case tKwAlias:
		return "'alias'"
	case tKwQueried:
		return "'queried'"
	case tKwProduced:
		return "'produced'"
	}
	return "unknown"
}
