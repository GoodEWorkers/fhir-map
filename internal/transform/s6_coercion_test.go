package transform

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

func TestEngine_Conformance_BadDateTime_AcceptedAndLogged(t *testing.T) {
	logger, buf := errLogger()
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	got, err := New(WithConformanceLogging(logger)).Transform(context.Background(), sm, map[string]any{"value": "not-a-date"})
	require.NoError(t, err, "non-coercible datetime must be accepted, not error, when conformance logging is on")
	assert.Equal(t, "not-a-date", got.(map[string]any)["out"], "value kept uncoerced (lenient accept)")

	log := buf.String()
	assert.Contains(t, log, "HL7V2_NONCONFORMANT_ACCEPTED")
	assert.Contains(t, log, "coercion-failed")
	assert.Contains(t, log, "toDateTime")
	assert.NotContains(t, log, "not-a-date", "raw value must NOT be logged (PHI-safe)")
}

func TestEngine_Conformance_BadDateTime_Off_FailsClosed(t *testing.T) {
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{{ValueID: "v"}}, true)
	_, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "not-a-date"})
	require.Error(t, err, "without conformance logging, a coercion failure stays fail-closed (may be a map bug)")
}
