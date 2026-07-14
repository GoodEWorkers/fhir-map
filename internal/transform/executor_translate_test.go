package transform

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
	"github.com/goodeworkers/fhir-map/internal/translate"
	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

type translatorStub struct {
	resp *translate.Response
	err  error
	last translate.Request
}

func (s *translatorStub) Translate(_ context.Context, req translate.Request) (*translate.Response, error) {
	s.last = req
	return s.resp, s.err
}

func newTranslateSM(outputType string) *structuremap.StructureMap {
	return &structuremap.StructureMap{
		ResourceType: "StructureMap",
		Name:         "TranslateNilConcept",
		Status:       "active",
		Group: []structuremap.Group{{
			Name: "g",
			Input: []structuremap.Input{
				{Name: "src", Mode: "source"},
				{Name: "tgt", Mode: "target"},
			},
			Rule: []structuremap.Rule{{
				Name: "r",
				Source: []structuremap.Source{{
					Context: "src", Element: "code", Variable: "v",
				}},
				Target: []structuremap.Target{{
					Context: "tgt", Element: "gender", Transform: "translate",
					Parameter: []structuremap.Parameter{
						{ValueID: "v"},
						{ValueString: "http://example.org/cm"},
						{ValueString: outputType},
					},
				}},
			}},
		}},
	}
}

// TestExecutor_Translate_CodingOutputType_ConceptNilReturnsError asserts that Coding output
// with a nil Concept errors explicitly, not silently; this is a spec deviation fix.
func TestExecutor_Translate_CodingOutputType_ConceptNilReturnsError(t *testing.T) {
	stub := &translatorStub{
		resp: &translate.Response{
			Result:  true,
			Matches: []fhir.TranslateMatch{{Relationship: "equivalent", Concept: nil}},
		},
	}
	sm := newTranslateSM("Coding")
	source := map[string]any{"resourceType": "Patient", "code": "M"}

	_, err := NewEngine(stub).Transform(t.Context(), sm, source)
	require.Error(t, err, "Coding output with nil Concept must surface an error")
	msg := err.Error()
	assert.Contains(t, msg, "match has no concept")
	assert.Contains(t, msg, "M")
	assert.Contains(t, msg, "http://example.org/cm")
}

// TestExecutor_Translate_CodeableConceptOutputType_ConceptNilReturnsError asserts the same guard as Coding.
func TestExecutor_Translate_CodeableConceptOutputType_ConceptNilReturnsError(t *testing.T) {
	stub := &translatorStub{
		resp: &translate.Response{
			Result:  true,
			Matches: []fhir.TranslateMatch{{Relationship: "equivalent", Concept: nil}},
		},
	}
	sm := newTranslateSM("CodeableConcept")
	source := map[string]any{"resourceType": "Patient", "code": "F"}

	_, err := NewEngine(stub).Transform(t.Context(), sm, source)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "match has no concept"),
		"diagnostic must include 'match has no concept'; got: %v", err)
}

// TestExecutor_Translate_CodeOutputType_ConceptNil_ReturnsEmptyString asserts that code output with nil Concept yields empty string (spec-compliant unmapped code).
func TestExecutor_Translate_CodeOutputType_ConceptNil_ReturnsEmptyString(t *testing.T) {
	stub := &translatorStub{
		resp: &translate.Response{
			Result:  true,
			Matches: []fhir.TranslateMatch{{Relationship: "equivalent", Concept: nil}},
		},
	}
	sm := newTranslateSM("code")
	source := map[string]any{"resourceType": "Patient", "code": "M"}

	result, err := NewEngine(stub).Transform(t.Context(), sm, source)
	require.NoError(t, err, "code output with nil Concept must NOT error (legitimate unmapped)")
	target := result.(map[string]any)
	assert.Equal(t, "", target["gender"], "nil Concept under outputType=code maps to empty string")
}
