package transform_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/transform"
	"github.com/goodeworkers/fhir-map/internal/transform/fml"
	"github.com/goodeworkers/fhir-map/internal/transform/resolver"
)

func TestAliasIntegration_FMLParseThenEngineRun(t *testing.T) {
	src := `map "http://example.org/fml/uses" = "Uses"
uses "http://hl7.org/fhir/StructureDefinition/QuestionnaireResponse" alias QR as source
uses "http://hl7.org/fhir/StructureDefinition/Patient" alias P as target
group main(source qr : QR, target p : P) {
  qr.name as n -> p.name = copy(n);
}
`
	sm, err := fml.Parse(src)
	require.NoError(t, err)

	t.Logf("Structure count: %d", len(sm.Structure))
	for i, s := range sm.Structure {
		t.Logf("  structure[%d]: URL=%q Alias=%q Mode=%q", i, s.URL, s.Alias, s.Mode)
	}
	t.Logf("Group[0].Input[0].Type: %q", sm.Group[0].Input[0].Type)

	tr := resolver.NewResolver(nil) // uses only bundled HL7 fixture
	eng := transform.New(transform.WithTypeResolver(tr))

	source := map[string]any{"resourceType": "QuestionnaireResponse", "name": "Ada"}
	result, err := eng.Transform(context.Background(), sm, source)
	require.NoError(t, err)
	assert.Equal(t, "Ada", result.(map[string]any)["name"])
}
