package hl7base

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHL7BaseFixture_LoadsWithoutPanic(t *testing.T) {
	// Package init already ran; if we reach here the embed parsed successfully.
	require.NotNil(t, BaseTypes, "BaseTypes must be populated by package init")
	assert.NotEmpty(t, BaseTypes, "BaseTypes must contain at least one entry")
}

func TestHL7BaseFixture_ContainsPatient(t *testing.T) {
	sd, ok := BaseTypes["http://hl7.org/fhir/StructureDefinition/Patient"]
	require.True(t, ok, "fixture must contain Patient canonical URL")
	assert.Equal(t, "Patient", sd.Type)
	assert.Equal(t, "resource", sd.Kind)
}

func TestHL7BaseFixture_ContainsStringPrimitive(t *testing.T) {
	sd, ok := BaseTypes["http://hl7.org/fhir/StructureDefinition/string"]
	require.True(t, ok, "fixture must contain string canonical URL")
	assert.Equal(t, "string", sd.Type)
	assert.Equal(t, "primitive-type", sd.Kind)
}

func TestHL7BaseFixture_ContainsResource(t *testing.T) {
	sd, ok := BaseTypes["http://hl7.org/fhir/StructureDefinition/Resource"]
	require.True(t, ok, "fixture must contain Resource abstract type")
	assert.Equal(t, "Resource", sd.Type)
}

func TestHL7BaseFixture_ContainsAllPrimitives(t *testing.T) {
	primitives := []string{
		"boolean", "integer", "integer64", "decimal", "base64Binary",
		"instant", "date", "dateTime", "time", "code", "oid", "id",
		"markdown", "unsignedInt", "positiveInt", "uuid", "uri", "url",
		"canonical", "string", "xhtml",
	}
	for _, p := range primitives {
		url := "http://hl7.org/fhir/StructureDefinition/" + p
		sd, ok := BaseTypes[url]
		require.True(t, ok, "fixture must contain primitive type %q", p)
		assert.Equal(t, p, sd.Type, "primitive %q must have matching type field", p)
		assert.Equal(t, "primitive-type", sd.Kind, "primitive %q must have kind=primitive-type", p)
	}
}

func TestHL7BaseFixture_ContainsCommonResources(t *testing.T) {
	resources := []string{
		"Patient", "Practitioner", "Observation", "Encounter", "Condition",
		"Procedure", "MedicationRequest", "Bundle", "Organization",
	}
	for _, r := range resources {
		url := "http://hl7.org/fhir/StructureDefinition/" + r
		_, ok := BaseTypes[url]
		assert.True(t, ok, "fixture must contain resource %q", r)
	}
}
