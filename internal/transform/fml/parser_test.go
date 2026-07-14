package fml

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FML lexer + parser: converts FHIR Mapping Language text into StructureMap AST.

func TestFML_Parse_MinimalMap(t *testing.T) {
	src := `
		map "http://example.org/sm/test" = "TestMap"

		group MapIt(source src, target tgt) {
		  src.value as v -> tgt.out = copy(v);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	assert.Equal(t, "http://example.org/sm/test", sm.URL)
	assert.Equal(t, "TestMap", sm.Name)
	require.Len(t, sm.Group, 1)
	g := sm.Group[0]
	assert.Equal(t, "MapIt", g.Name)
	require.Len(t, g.Input, 2)
	assert.Equal(t, "src", g.Input[0].Name)
	assert.Equal(t, "source", g.Input[0].Mode)
	assert.Equal(t, "tgt", g.Input[1].Name)
	assert.Equal(t, "target", g.Input[1].Mode)
	require.Len(t, g.Rule, 1)
	r := g.Rule[0]
	require.Len(t, r.Source, 1)
	assert.Equal(t, "src", r.Source[0].Context)
	assert.Equal(t, "value", r.Source[0].Element)
	assert.Equal(t, "v", r.Source[0].Variable)
	require.Len(t, r.Target, 1)
	assert.Equal(t, "tgt", r.Target[0].Context)
	assert.Equal(t, "out", r.Target[0].Element)
	assert.Equal(t, "copy", r.Target[0].Transform)
	require.Len(t, r.Target[0].Parameter, 1)
	assert.Equal(t, "v", r.Target[0].Parameter[0].ValueID)
}

func TestFML_Parse_QRToPatient(t *testing.T) {
	src := `
		map "http://example.org/sm/qr-to-patient" = "QRToPatient"

		group MapQRtoPatient(source src : QuestionnaireResponse, target tgt : Patient) {
		  src.item where (linkId = 'first') as i ->
		    tgt.firstName = copy(i.answer.valueString) "firstName";

		  src.item where (linkId = 'last') as i ->
		    tgt.lastName = copy(i.answer.valueString) "lastName";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	g := sm.Group[0]
	require.Len(t, g.Rule, 2)

	// First rule: src.item where (linkId = 'first') as i -> tgt.firstName = copy(i.answer.valueString)
	first := g.Rule[0]
	require.Len(t, first.Source, 1)
	assert.Equal(t, "src", first.Source[0].Context)
	assert.Equal(t, "item", first.Source[0].Element)
	assert.Equal(t, "linkId = 'first'", first.Source[0].Condition)
	assert.Equal(t, "i", first.Source[0].Variable)
	require.Len(t, first.Target, 1)
	assert.Equal(t, "firstName", first.Target[0].Element)
	assert.Equal(t, "copy", first.Target[0].Transform)
	require.Len(t, first.Target[0].Parameter, 1)
	assert.Equal(t, "i.answer.valueString", first.Target[0].Parameter[0].ValueID)
	assert.Equal(t, "firstName", first.Name)
}

// FML implicit-copy shorthand: bare variable RHS (tgt.x = v;) desugar to copy(v).

func TestFML_Parse_ImplicitCopy(t *testing.T) {
	src := `
		map "http://example.org/sm/implicit" = "Implicit"

		group G(source s, target t) {
		  s.value as v -> t.out = v;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	tgt := sm.Group[0].Rule[0].Target
	require.Len(t, tgt, 1)
	assert.Equal(t, "out", tgt[0].Element)
	assert.Equal(t, "copy", tgt[0].Transform, "bare identifier RHS must synthesise transform=copy")
	require.Len(t, tgt[0].Parameter, 1)
	assert.Equal(t, "v", tgt[0].Parameter[0].ValueID)
}

// Parser must not confuse `as` keyword (after implicit copy) for a transform argument.
func TestFML_Parse_ImplicitCopyThenAs(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.v as v -> t.out = v as bound;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	rule := sm.Group[0].Rule[0]
	require.Len(t, rule.Target, 1)
	assert.Equal(t, "copy", rule.Target[0].Transform)
	assert.Equal(t, "v", rule.Target[0].Parameter[0].ValueID)
	assert.Equal(t, "bound", rule.Target[0].Variable, "implicit copy must not consume the `as` keyword")
}

// Decimal literals go to Parameter.ValueDecimal as float64, integers to ValueInteger.
func TestFML_Parse_DecimalArg_AsValueDecimal(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.value as v -> t.out = copy(1.5);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	p := sm.Group[0].Rule[0].Target[0].Parameter[0]
	require.NotNil(t, p.ValueDecimal, "decimal arg should populate ValueDecimal")
	assert.Equal(t, 1.5, *p.ValueDecimal)
	assert.Empty(t, p.ValueString, "decimal arg must not be stringified into ValueString")
	assert.Nil(t, p.ValueInteger)
}

func TestFML_Parse_IntegerArg_StillValueInteger(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.value as v -> t.out = copy(3);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	p := sm.Group[0].Rule[0].Target[0].Parameter[0]
	require.NotNil(t, p.ValueInteger)
	assert.Equal(t, 3, *p.ValueInteger)
	assert.Nil(t, p.ValueDecimal)
}

// Bare negative literals in copy() args apply sign directly to ValueInteger/ValueDecimal.
func TestFML_Parse_NegativeIntegerArg(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v -> t.out = copy(-5); }`)
	require.NoError(t, err)
	p := sm.Group[0].Rule[0].Target[0].Parameter[0]
	require.NotNil(t, p.ValueInteger)
	assert.Equal(t, -5, *p.ValueInteger)
	assert.Nil(t, p.ValueDecimal)
}

func TestFML_Parse_NegativeDecimalArg(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v -> t.out = copy(-1.5); }`)
	require.NoError(t, err)
	p := sm.Group[0].Rule[0].Target[0].Parameter[0]
	require.NotNil(t, p.ValueDecimal)
	assert.Equal(t, -1.5, *p.ValueDecimal)
	assert.Nil(t, p.ValueInteger)
}

func TestFML_Parse_PositiveSignedArg(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v -> t.out = copy(+7); }`)
	require.NoError(t, err)
	p := sm.Group[0].Rule[0].Target[0].Parameter[0]
	require.NotNil(t, p.ValueInteger)
	assert.Equal(t, 7, *p.ValueInteger)
}

// FML lexer must tokenize comparison operators (<, <=, >, >=) in where conditions.
func TestFML_Parse_WhereWithComparisonOperators(t *testing.T) {
	for _, op := range []string{"<", "<=", ">", ">="} {
		src := `map "u"="n" group G(source s, target t){ s.v as v where (v ` + op + ` 5) -> t.out = copy(v); }`
		sm, err := Parse(src)
		require.NoError(t, err, "op %q should lex+parse", op)
		cond := sm.Group[0].Rule[0].Source[0].Condition
		assert.Contains(t, cond, op, "captured condition should preserve %q", op)
	}
}

// Comparison and negative literals together in where conditions.
func TestFML_Parse_WhereWithComparisonAndNegative(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v where (v >= -1.5) -> t.out = copy(v); }`)
	require.NoError(t, err)
	cond := sm.Group[0].Rule[0].Source[0].Condition
	assert.Contains(t, cond, ">=")
	assert.Contains(t, cond, "1.5")
}

// FML lexer must tokenize FHIRPath special variables ($this, $index) in where conditions.
func TestFML_Parse_WhereWithDollarThis(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v where ($this = 'ORU') -> t.out = copy(v); }`)
	require.NoError(t, err)
	cond := sm.Group[0].Rule[0].Source[0].Condition
	assert.Contains(t, cond, "$this")
}

func TestFML_Parse_WhereWithDollarIndex(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.v as v where ($index >= 1) -> t.out = copy(v); }`)
	require.NoError(t, err)
	cond := sm.Group[0].Rule[0].Source[0].Condition
	assert.Contains(t, cond, "$index")
}

// Source check(expr) clause parses into Source.Check.
func TestFML_Parse_SourceCheck(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v check ($this > 0) -> t.out = copy(v); }`)
	require.NoError(t, err)
	assert.Equal(t, "$this > 0", sm.Group[0].Rule[0].Source[0].Check)
}

// check coexists with where and as in any order.
func TestFML_Parse_SourceCheckWithWhere(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v where ($this >= 1) check ($this > 0) -> t.out = copy(v); }`)
	require.NoError(t, err)
	src := sm.Group[0].Rule[0].Source[0]
	assert.Equal(t, "$this >= 1", src.Condition)
	assert.Equal(t, "$this > 0", src.Check)
}

// Source list modes (first/last/not_first/not_last/only_one) lower to ListMode using FHIR R5 wire codes.
func TestFML_Parse_SourceListModes(t *testing.T) {
	cases := map[string]string{
		"first":     "first",
		"last":      "last",
		"not_first": "not-first",
		"not_last":  "not-last",
		"only_one":  "only-one",
	}
	for fmlWord, wireCode := range cases {
		src := `map "u"="n" group G(source s, target t){ s.item ` + fmlWord + ` as v -> t.out = copy(v); }`
		sm, err := Parse(src)
		require.NoError(t, err, "list mode %q", fmlWord)
		assert.Equal(t, wireCode, sm.Group[0].Rule[0].Source[0].ListMode, "FML %q", fmlWord)
	}
}

// Non-list-mode identifiers after element must not be misread as list modes.
func TestFML_Parse_NonListModeIdentNotConsumed(t *testing.T) {
	// `as v` follows the element directly; `v` is a variable, not a list mode.
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.item as v -> t.out = copy(v); }`)
	require.NoError(t, err)
	src := sm.Group[0].Rule[0].Source[0]
	assert.Equal(t, "v", src.Variable)
	assert.Empty(t, src.ListMode)
}

// Source element type (context.element : Type) lowers to Source.Type.
func TestFML_Parse_SourceElementType(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value : integer as v -> t.out = copy(v); }`)
	require.NoError(t, err)
	assert.Equal(t, "integer", sm.Group[0].Rule[0].Source[0].Type)
}

// Type precedes cardinality in source element annotations.
func TestFML_Parse_SourceElementTypeWithCardinality(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value : Quantity 0..1 as v -> t.out = copy(v); }`)
	require.NoError(t, err)
	src := sm.Group[0].Rule[0].Source[0]
	assert.Equal(t, "Quantity", src.Type)
	require.NotNil(t, src.Min)
	assert.Equal(t, 0, *src.Min)
	assert.Equal(t, "1", src.Max)
}

// Target list modes (first/last/single/collate) lower to Target.ListMode; share carries a rule id.
func TestFML_Parse_TargetListModes(t *testing.T) {
	for _, mode := range []string{"first", "last", "single", "collate"} {
		src := `map "u"="n" group G(source s, target t){ s.value as v -> t.out = copy(v) ` + mode + `; }`
		sm, err := Parse(src)
		require.NoError(t, err, "target list mode %q", mode)
		assert.Equal(t, []string{mode}, sm.Group[0].Rule[0].Target[0].ListMode)
	}
}

// share <ruleId> may follow as in target variable binding.
func TestFML_Parse_TargetShareStillWorks(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v -> t.out = copy(v) as x share r1; }`)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	assert.Equal(t, "x", tgt.Variable)
	assert.Equal(t, []string{"share"}, tgt.ListMode)
	assert.Equal(t, "r1", tgt.ListRuleId)
}

// Source default([value]) lowers to Source.DefaultValue (JSON) and Source.DefaultValueType.
func TestFML_Parse_SourceDefault(t *testing.T) {
	cases := []struct {
		literal  string
		wantJSON string
		wantType string
	}{
		{"0", "0", "integer"},
		{"1.5", "1.5", "decimal"},
		{"'fallback'", `"fallback"`, "string"},
		{"true", "true", "boolean"},
		{"-3", "-3", "integer"},
	}
	for _, c := range cases {
		src := `map "u"="n" group G(source s, target t){ s.value default (` + c.literal + `) as v -> t.out = copy(v); }`
		sm, err := Parse(src)
		require.NoError(t, err, "default %s", c.literal)
		s := sm.Group[0].Rule[0].Source[0]
		assert.JSONEq(t, c.wantJSON, string(s.DefaultValue), "default %s value", c.literal)
		assert.Equal(t, c.wantType, s.DefaultValueType, "default %s type", c.literal)
	}
}

// evaluate(context, expr) captures expr as raw FHIRPath expression, not a literal.
func TestFML_Parse_ExplicitEvaluate_CapturesExpression(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v -> t.out = evaluate(v, $this + 2); }`)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	assert.Equal(t, "evaluate", tgt.Transform)
	require.Len(t, tgt.Parameter, 2)
	assert.Equal(t, "v", tgt.Parameter[0].ValueID, "1st arg is the context variable")
	assert.Contains(t, tgt.Parameter[1].ValueString, "$this + 2", "2nd arg is the captured expression")
}

// Quoted evaluate(v, 'name') unquotes the string so engine evaluates FHIRPath, not string literal.
func TestFML_Parse_ExplicitEvaluate_QuotedExprUnquoted(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v -> t.out = evaluate(v, 'name'); }`)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	require.Len(t, tgt.Parameter, 2)
	assert.Equal(t, "name", tgt.Parameter[1].ValueString, "quoted evaluate arg must be unquoted to the bare FHIRPath")
}

// evaluate() with nested parens (e.g. iif() function calls) balanced by paren tracking.
func TestFML_Parse_ExplicitEvaluate_NestedParens(t *testing.T) {
	sm, err := Parse(`map "u"="n" group G(source s, target t){ s.value as v -> t.out = evaluate(v, iif($this > 0, 'pos', 'neg')); }`)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	require.Len(t, tgt.Parameter, 2)
	assert.Contains(t, tgt.Parameter[1].ValueString, "iif")
	assert.Contains(t, tgt.Parameter[1].ValueString, "'pos'")
}

func TestFML_Parse_ExplicitCopyStillWorks(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.value as v -> t.out = copy(v);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	assert.Equal(t, "copy", tgt.Transform)
	assert.Equal(t, "v", tgt.Parameter[0].ValueID)
}

// FML allows as...where and where...as orderings on source bindings interchangeably.
func TestFML_Parse_SourceAsBeforeWhere(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.item as i where (linkId = 'name') -> t.name = copy(i.answer.valueString);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Source, 1)
	assert.Equal(t, "s", r.Source[0].Context)
	assert.Equal(t, "item", r.Source[0].Element)
	assert.Equal(t, "i", r.Source[0].Variable)
	assert.Equal(t, "linkId = 'name'", r.Source[0].Condition)
}

// uses "url" alias N as source/target must parse; source/target are mode identifiers here, not keywords.
func TestFML_Parse_UsesAsSourceTarget(t *testing.T) {
	src := `
		map "http://example.org/sm/uses" = "UsesAsModes"

		uses "http://hl7.org/fhir/StructureDefinition/Patient" alias Pat as source
		uses "http://hl7.org/fhir/StructureDefinition/Person" alias Per as target

		group G(source s : Pat, target t : Per) {
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Structure, 2)
	assert.Equal(t, "source", sm.Structure[0].Mode)
	assert.Equal(t, "Pat", sm.Structure[0].Alias)
	assert.Equal(t, "target", sm.Structure[1].Mode)
	assert.Equal(t, "Per", sm.Structure[1].Alias)
}

// let v = expr; is a local variable binding, represented as synthetic Rule with Source.
func TestFML_Parse_Let(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  let n = s.name;
		  s.x as v -> t.y = copy(v);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	g := sm.Group[0]
	require.Len(t, g.Rule, 2, "let statement must appear as a synthetic rule alongside the regular rule")
	letRule := g.Rule[0]
	require.Len(t, letRule.Source, 1)
	assert.Equal(t, "n", letRule.Source[0].Variable, "let binding name lands on Source.Variable")
	assert.Equal(t, "s", letRule.Source[0].Context, "let expression's head ident lands on Source.Context")
	assert.Equal(t, "name", letRule.Source[0].Element, "let expression's dotted tail lands on Source.Element")
	assert.Empty(t, letRule.Target, "let synthesises no Target")
}

// then { nested-rules } is an anonymous nested-group form; rules populate Rule.Rule.
func TestFML_Parse_ThenAnonymousBlock(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.item as i -> t.entry as e then {
		    i.value as v -> e.out = copy(v);
		  };
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	parent := sm.Group[0].Rule[0]
	require.Len(t, parent.Rule, 1, "then { ... } body must populate parent Rule.Rule")
	nested := parent.Rule[0]
	require.Len(t, nested.Source, 1)
	assert.Equal(t, "i", nested.Source[0].Context)
	assert.Equal(t, "value", nested.Source[0].Element)
	require.Len(t, nested.Target, 1)
	assert.Equal(t, "out", nested.Target[0].Element)
}

// then GroupName(args) is explicit dependent-group form; populates Rule.Dependent.
func TestFML_Parse_ThenDependent(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.x as v -> t.y as w then Sub(v, w);
		}

		group Sub(source a, target b) {
		  a.q -> b.r;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 2)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Dependent, 1, "then Name(args) populates Rule.Dependent")
	assert.Equal(t, "Sub", r.Dependent[0].Name)
	require.Len(t, r.Dependent[0].Parameter, 2)
	assert.Equal(t, "v", r.Dependent[0].Parameter[0].ValueID)
	assert.Equal(t, "w", r.Dependent[0].Parameter[1].ValueID)
}

// <<types>> and <<type+types>> after parameter list set Group.TypeMode.
func TestFML_Parse_GroupTypeMode(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) <<types>> {
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	assert.Equal(t, "types", sm.Group[0].TypeMode)
}

func TestFML_Parse_GroupTypeAndTypes(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) <<type+types>> {
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	assert.Equal(t, "type+types", sm.Group[0].TypeMode)
}

// /// documentation comments attach to the next group or rule.
func TestFML_Parse_DocCommentOnGroup(t *testing.T) {
	src := `
		map "u" = "n"

		/// Maps Foo to Bar
		group G(source s, target t) {
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	assert.Equal(t, "Maps Foo to Bar", sm.Group[0].Documentation)
}

func TestFML_Parse_DocCommentOnRule(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  /// First rule
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	assert.Equal(t, "First rule", sm.Group[0].Rule[0].Documentation)
}

// Backtick-quoted identifiers allow FML to name elements that tokenize oddly.
func TestFML_Parse_BacktickIdent(t *testing.T) {
	src := "map \"u\" = \"n\"\n\ngroup G(source s, target t) {\n  s.`weird name` as v -> t.out = copy(v);\n}\n"
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Source, 1)
	assert.Equal(t, "weird name", r.Source[0].Element)
}

// Parenthesised FHIRPath expression in target (v.a + v.b) synthesises transform=evaluate.
func TestFML_Parse_TargetFhirPathExpr(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.x as v -> t.y = (v.a + v.b);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	tgt := sm.Group[0].Rule[0].Target[0]
	assert.Equal(t, "evaluate", tgt.Transform, "(expr) form must synthesise transform=evaluate")
	require.Len(t, tgt.Parameter, 1)
	assert.Equal(t, "v.a + v.b", strings.TrimSpace(tgt.Parameter[0].ValueString))
}

// Source cardinality (min..max) sets Source.Min and Source.Max; * encoded as "*" string.
func TestFML_Parse_SourceCardinality(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.item 0..* as v -> t.out = copy(v);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Source, 1)
	require.NotNil(t, r.Source[0].Min)
	assert.Equal(t, 0, *r.Source[0].Min)
	assert.Equal(t, "*", r.Source[0].Max)
}

func TestFML_Parse_SourceCardinalityIntegerMax(t *testing.T) {
	src := `
		map "u" = "n"

		group G(source s, target t) {
		  s.item 1..1 as v -> t.out = copy(v);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	require.NotNil(t, r.Source[0].Min)
	assert.Equal(t, 1, *r.Source[0].Min)
	assert.Equal(t, "1", r.Source[0].Max)
}

// Inline conceptmap declaration; translate() refs via fragment URL; parser folds prefixes to URIs.
func TestFML_Parse_InlineConceptMap(t *testing.T) {
	src := `
		map "http://example.org/sm/with-inline-cm" = "WithInlineCM"

		conceptmap "#gender" {
		  prefix s = "http://hl7.org/fhir/administrative-gender"
		  prefix t = "http://example.org/gender"

		  s:male - t:M
		  s:female == t:F
		  s:unknown != t:U
		}

		group G(source src, target tgt) {
		  src.gender as v -> tgt.gender = translate(v, '#gender', 'code');
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Contained, 1, "inline conceptmap must produce one contained map")
	cm := sm.Contained[0]
	assert.Equal(t, "ConceptMap", cm.ResourceType)
	assert.Equal(t, "#gender", cm.URL)
	assert.Equal(t, "gender", cm.ID, "fragment URL must populate the contained id")
	require.Len(t, cm.Group, 1, "all rows share a (source,target) — one group")
	g := cm.Group[0]
	assert.Equal(t, "http://hl7.org/fhir/administrative-gender", g.Source)
	assert.Equal(t, "http://example.org/gender", g.Target)
	require.Len(t, g.Element, 3)
	assert.Equal(t, "male", g.Element[0].Code)
	require.Len(t, g.Element[0].Target, 1)
	assert.Equal(t, "M", g.Element[0].Target[0].Code)
	assert.Equal(t, "related-to", g.Element[0].Target[0].Relationship)
	assert.Equal(t, "equivalent", g.Element[1].Target[0].Relationship)
	assert.Equal(t, "not-related-to", g.Element[2].Target[0].Relationship)
}

// Bare-name conceptmap sets cm.ID for #-reference lookup in translate() calls.
func TestParseConceptMap_BareNameSetsContainedID(t *testing.T) {
	src := `
		map "http://qa.test/sm/sex" = "sex"

		conceptmap "sex-cm" {
		  prefix s = "http://qa.test/src"
		  prefix t = "http://qa.test/tgt"
		  s:M == t:male
		}

		group g(source src, target tgt) {
		  src.sex as v -> tgt.gender = translate(v, "#sex-cm", "code");
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Contained, 1)
	cm := sm.Contained[0]
	assert.Equal(t, "sex-cm", cm.URL, "bare-name URL stored as-is")
	assert.Equal(t, "sex-cm", cm.ID, "bare-name must populate cm.ID for #-reference lookup")
}

// Quoted codes (dashes, digits, punctuation) in conceptmap must round-trip unchanged.
func TestFML_Parse_InlineConceptMap_QuotedCodes(t *testing.T) {
	src := `
		map "u" = "n"

		conceptmap "http://example.org/cm/q" {
		  prefix s = "http://src"
		  prefix t = "http://tgt"

		  s:"weird-code-1" - t:"OUT-A"
		  s:"123" == t:"456"
		}

		group G(source s, target t) {
		  s.x -> t.y;
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Contained, 1)
	cm := sm.Contained[0]
	assert.Equal(t, "http://example.org/cm/q", cm.URL)
	assert.Empty(t, cm.ID, "non-fragment URLs do not auto-populate id")
	require.Len(t, cm.Group, 1)
	require.Len(t, cm.Group[0].Element, 2)
	assert.Equal(t, "weird-code-1", cm.Group[0].Element[0].Code)
	assert.Equal(t, "OUT-A", cm.Group[0].Element[0].Target[0].Code)
}

// Undeclared prefix in conceptmap is a hard parse error; silent fallthrough would break translate().
func TestFML_Parse_InlineConceptMap_UndeclaredPrefix(t *testing.T) {
	src := `
		map "u" = "n"

		conceptmap "#m" {
		  prefix s = "http://src"
		  s:a - missing:b
		}

		group G(source s, target t) {
		  s.x -> t.y;
		}
	`
	_, err := Parse(src)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "undeclared prefix")
}

// FML comments (// line, /* block */) must be silently dropped by lexer.
func TestFML_Parse_Comments(t *testing.T) {
	src := `
		// Top-level comment
		map "u" = "n"

		/* block comment
		   spanning lines */
		group G(source s, target t) {
		  s.x -> t.y; // inline
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	assert.Equal(t, "u", sm.URL)
	require.Len(t, sm.Group, 1)
}

func TestFML_Parse_TypePlusSingular(t *testing.T) {
	// <<type+>> singular type-mode marker on a group.
	src := `
		map "u" = "n"
		group string(source src : string, target tgt : string) <<type+>> {
		  src.value as v -> tgt.value = v "stringValue";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	assert.Equal(t, "type+", sm.Group[0].TypeMode)
}

func TestFML_Parse_PercentVarReference(t *testing.T) {
	// FHIRPath %variable references inside parenthesised expressions.
	src := `
		map "u" = "n"
		uses "http://hl7.org/fhir/StructureDefinition/QuestionnaireResponse" alias QR as source
		uses "http://hl7.org/fhir/StructureDefinition/Patient" alias Patient as target
		group G(source src : QR, target tgt : Patient) {
		  src.ext as v -> tgt.birthDate = (%v + 5) "plus";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group[0].Rule, 1)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Target, 1)
	assert.Equal(t, "evaluate", r.Target[0].Transform)
	assert.Contains(t, r.Target[0].Parameter[0].ValueString, "%v")
}

func TestFML_Parse_StringLiteralTargetAssignment(t *testing.T) {
	// tgt.x = 'literal' desugars to copy(literal).
	src := `
		map "u" = "n"
		group G(source src, target tgt) {
		  src -> tgt.gender = 'female' "Simple Assignment";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Target, 1)
	assert.Equal(t, "copy", r.Target[0].Transform)
	require.Len(t, r.Target[0].Parameter, 1)
	assert.Equal(t, "female", r.Target[0].Parameter[0].ValueString)
}

func TestFML_Parse_DocCommentMetadataHeader(t *testing.T) {
	// /// metadata lines can substitute for map declaration.
	src := `
/// url = "http://example.org/syntax"
/// name = "syntax"

uses "http://hl7.org/fhir/StructureDefinition/Patient" alias Patient as source
uses "http://hl7.org/fhir/StructureDefinition/Basic" alias Basic as target

group G(source src : Patient, target tgt : Basic) {
  src.identifier -> tgt.identifer;
}
`
	sm, err := Parse(src)
	require.NoError(t, err)
	assert.Equal(t, "http://example.org/syntax", sm.URL)
	assert.Equal(t, "syntax", sm.Name)
}

func TestFML_Parse_SourceOnlyThenBlock(t *testing.T) {
	// Source-only rule (no ->) with then { } block.
	src := `
		map "u" = "n"
		group G(source src, target tgt) {
		  src.ext as ext where (url = 'x') then {
		    ext.value as v -> tgt.x = v "inner";
		  } "outer";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	assert.Empty(t, r.Target, "source-only rule has no targets")
	assert.NotEmpty(t, r.Rule, "nested rules from then{} block")
}

func TestFML_Parse_WhereWithoutParens(t *testing.T) {
	// where clause without parentheses.
	src := `
		map "u" = "n"
		group G(source src, target tgt) {
		  src.item as item where linkId.value in ('x') -> tgt.gender = (item.answer.valueString);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	assert.NotEmpty(t, r.Source[0].Condition)
	assert.Contains(t, r.Source[0].Condition, "linkId")
}

func TestFML_Parse_LogSourceClause(t *testing.T) {
	// log(expr) source clause between where and target.
	src := `
		map "u" = "n"
		group G(source src, target tgt) {
		  src.item as item where (item.code != 'read') log(item.code) -> tgt.item as out then {
		    item.code as code -> out.code = code;
		  } "item";
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	assert.Equal(t, "item.code", r.Source[0].LogMessage)
}

func TestFML_Parse_ShareListMode(t *testing.T) {
	// share listRuleId list-mode directive on target variable.
	src := `
		map "u" = "n"
		group G(source src, target tgt) {
		  src.item as item where linkId.value = 'x' -> tgt.name as name share patientName then F(item, name);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	r := sm.Group[0].Rule[0]
	require.Len(t, r.Target, 1)
	assert.Equal(t, []string{"share"}, r.Target[0].ListMode)
	assert.Equal(t, "patientName", r.Target[0].ListRuleId)
}

// then map "<url>" group <name>(args) form; populates Dependent.MapURL and Dependent.Name.
func TestFML_Parse_ThenMapGroupForm(t *testing.T) {
	src := `
		map "http://example.org/A" = "MapA"
		group g(source src, target tgt) {
			src.id as v -> tgt.id = v then map "http://example.org/B" group copyId(src, tgt);
		}
	`
	sm, err := Parse(src)
	require.NoError(t, err)
	require.Len(t, sm.Group, 1)
	require.Len(t, sm.Group[0].Rule, 1)
	rule := sm.Group[0].Rule[0]
	require.Len(t, rule.Dependent, 1, "then map ... group must produce one Dependent")
	dep := rule.Dependent[0]
	assert.Equal(t, "http://example.org/B", dep.MapURL, "MapURL must carry the canonical URL")
	assert.Equal(t, "copyId", dep.Name, "Name must carry the group name")
	require.Len(t, dep.Parameter, 2)
	assert.Equal(t, "src", dep.Parameter[0].ValueID)
	assert.Equal(t, "tgt", dep.Parameter[1].ValueID)
}
