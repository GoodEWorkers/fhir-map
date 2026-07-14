package transform

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/conceptmap"
	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// S6 accepts non-conformant input (like unmapped codes) instead of failing, emitting a PHI-safe ERROR log when conformance logging is on. This matches the prod pipeline's tolerance for data-coverage gaps.

func translateSMWithInlineCM() *structuremap.StructureMap {
	sm := mapWithTransform("translate", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "#cm"}, {ValueString: "code"},
	}, true)
	sm.Contained = []*conceptmap.ConceptMap{{
		ID: "cm", URL: "#cm",
		Group: []conceptmap.Group{{
			Target:  "http://loinc.org",
			Element: []conceptmap.Element{{Code: "KNOWN", Target: []conceptmap.Target{{Code: "L1", Relationship: "equivalent"}}}},
		}},
	}}
	return sm
}

func errLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})), buf
}

// Flag ON: unmapped code is accepted and an ERROR is logged with PHI-safe context (code + map, no value).
func TestEngine_Conformance_UnmappedCode_AcceptedAndLogged(t *testing.T) {
	logger, buf := errLogger()
	eng := New(WithConformanceLogging(logger))
	got, err := eng.Transform(context.Background(), translateSMWithInlineCM(), map[string]any{"value": "UNKNOWN"})
	require.NoError(t, err, "unmapped code must be ACCEPTED, not error, when conformance logging is on")
	_, has := got.(map[string]any)["out"]
	assert.False(t, has, "the unmapped coding is dropped (accepted, not emitted)")

	log := buf.String()
	assert.Contains(t, log, "HL7V2_NONCONFORMANT_ACCEPTED")
	assert.Contains(t, log, "unmapped-code")
	assert.Contains(t, log, "UNKNOWN", "logs the (non-PHI) source code for coverage")
	assert.NotContains(t, log, `"level":"WARN"`, "must be ERROR level, per directive")
}

// Flag OFF: unmapped codes in a RESOLVED ConceptMap are treated as data-coverage gaps (accepted, not errors). An UNRESOLVABLE map still errors.
func TestEngine_Conformance_Off_DropsUnmappedSilently(t *testing.T) {
	got, err := NewEngine(nil).Transform(context.Background(), translateSMWithInlineCM(), map[string]any{"value": "UNKNOWN"})
	require.NoError(t, err, "unmapped code drops by default, even without conformance logging")
	_, has := got.(map[string]any)["out"]
	assert.False(t, has, "the unmapped coding is dropped")
}

func TestEngine_Conformance_MappedCode_Unaffected(t *testing.T) {
	logger, buf := errLogger()
	got, err := New(WithConformanceLogging(logger)).Transform(
		context.Background(), translateSMWithInlineCM(), map[string]any{"value": "KNOWN"})
	require.NoError(t, err)
	assert.Equal(t, "L1", got.(map[string]any)["out"], "mapped code translates normally")
	assert.Empty(t, buf.String(), "no conformance log for a conformant value")
}
