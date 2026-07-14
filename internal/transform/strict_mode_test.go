package transform

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

// Strict transform mode catches two silent data-loss cases: coercion failures and unmapped translate codes.

// (1) Coercion: a non-coercible value errors in strict mode...
func TestStrict_Coercion_BadDateTime_Errors(t *testing.T) {
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	_, err := New(WithStrictTransform(true)).Transform(context.Background(), sm, map[string]any{"value": "not-a-date"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNonConformantCoercion), "got %v", err)
	assert.NotContains(t, err.Error(), "not-a-date", "coercion error must not leak the source value (PHI-conservative)")
}

// ...and strict OVERRIDES conformance logging (which otherwise accepts+logs).
func TestStrict_Coercion_OverridesConformanceLogging(t *testing.T) {
	logger, _ := errLogger()
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	_, err := New(WithStrictTransform(true), WithConformanceLogging(logger)).
		Transform(context.Background(), sm, map[string]any{"value": "not-a-date"})
	require.Error(t, err, "strict must error even when conformance logging is on")
	assert.True(t, errors.Is(err, ErrNonConformantCoercion))
}

// (2) Translate: a resolved map with no mapping for the code errors in strict
// (the canonical silent-data-loss case — an unmapped LOINC dropped).
func TestStrict_Translate_UnmappedCode_Errors(t *testing.T) {
	_, err := New(WithStrictTransform(true)).
		Transform(context.Background(), translateSMWithInlineCM(), map[string]any{"value": "UNKNOWN"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTranslateNoMatch), "got %v", err)
	assert.NotContains(t, err.Error(), "UNKNOWN", "strict translate error must not leak the source code (PHI-conservative)")
}

// No over-fire: a MAPPED code still translates normally under strict.
func TestStrict_Translate_MappedCode_NoError(t *testing.T) {
	got, err := New(WithStrictTransform(true)).
		Transform(context.Background(), translateSMWithInlineCM(), map[string]any{"value": "KNOWN"})
	require.NoError(t, err)
	assert.Equal(t, "L1", got.(map[string]any)["out"])
}

// No over-fire: a plain copy (no coercion, no translate) is unaffected by strict.
func TestStrict_Copy_NoError(t *testing.T) {
	sm := mapWithTransform("copy", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := New(WithStrictTransform(true)).Transform(context.Background(), sm, map[string]any{"value": "x"})
	require.NoError(t, err)
	assert.Equal(t, "x", got.(map[string]any)["out"])
}
