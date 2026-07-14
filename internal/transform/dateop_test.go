package transform

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

func TestTransform_DateOp_ParseFormat(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyyMMddHHmmss"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "20260530090000"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.True(t, strings.HasPrefix(out, "2026-05-30T09:00:00"), "HL7 datetime parsed to FHIR dateTime, got %q", out)
}

func TestTransform_DateOp_ParseFormat_Instant(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyyMMddHHmmss"}, {ValueString: "instant"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "20260530090000"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Contains(t, out, "2026-05-30", "instant output is a valid dateTime, got %q", out)
}

func TestTransform_DateOp_ArithmeticStillWorks(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "+"}, {ValueString: "P1Y"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "2026-05-30"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Contains(t, out, "2027", "+P1Y arithmetic still works, got %q", out)
}

// Every accepted token must be translated to a real Go-layout fragment,
// not left as a literal character. 'a' and 'Z' used to slip through untranslated
// in lenient mode, causing UTC-suffixed data to match but offset values like "+0200" to fail.
func TestJavaPatternToGoLayout_TranslatesAllAcceptedTokens(t *testing.T) {
	cases := []struct{ pattern, want string }{
		{"yyyyMMddHHmmss", "20060102150405"},
		{"yyyy-MM-dd", "2006-01-02"},
		{"yyyyMMddHHmmss.SSS", "20060102150405.000"}, // HL7 DTM; '.' is Go's frac-sec token
		{"HH:mm:ss.SSS", "15:04:05.000"},
		{"yyMMdd", "060102"},
		{"yyyy-MM-ddTHH:mm:ssZ", "2006-01-02T15:04:05Z0700"}, // Z = literal 'Z' or numeric offset
		{"yyyyMMddHHmmssZ", "20060102150405Z0700"},
		{"hh:mm a", "03:04 PM"},
		{"h:mm a", "3:04 PM"},
		{"d/M/yy", "2/1/06"},
		{"HH:m:s", "15:4:5"},
	}
	for _, c := range cases {
		got, err := javaPatternToGoLayout(c.pattern)
		require.NoError(t, err, "pattern %q", c.pattern)
		assert.Equal(t, c.want, got, "pattern %q", c.pattern)
	}
}

// Unsupported tokens must fail loud, not silently mismatch. Bare "SSS" without
// a separator would become three literal zeros, matching only millisecond-000 values.
func TestJavaPatternToGoLayout_UnknownTokensFailLoud(t *testing.T) {
	for _, p := range []string{"MMM dd yyyy", "yyyy-MM-dd z", "HH:mm:ss.S", "EEE", "yyyyQQ", "yyy", "yyyyMMddHHmmssSSS"} {
		_, err := javaPatternToGoLayout(p)
		require.Error(t, err, "pattern %q must be rejected, not silently mismatched", p)
		assert.Contains(t, err.Error(), "unsupported date-format token")
	}
}

// Regression: offset-bearing HL7 datetimes must parse and normalise to UTC
// (previously "+0200" values failed due to literal 'Z' in the layout).
func TestTransform_DateOp_ParseFormat_WithOffset(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyyMMddHHmmssZ"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "20260530090000+0200"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Equal(t, "2026-05-30T07:00:00Z", out, "offset must be honoured and normalised to UTC")
}

// The Z0700 pattern must accept both literal 'Z' and numeric offsets
// to avoid regressing previously-working UTC data.
func TestTransform_DateOp_ParseFormat_WithUTCZSuffix(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyyMMddHHmmssZ"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "20260530090000Z"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Equal(t, "2026-05-30T09:00:00Z", out, "literal-Z UTC suffix must still parse")
}

func TestTransform_DateOp_ParseFormat_WithMillis(t *testing.T) {
	sm := mapWithTransform("dateOp", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyyMMddHHmmss.SSS"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "20260530090000.123"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Equal(t, "2026-05-30T09:00:00Z", out, "millisecond DTM must parse (fraction dropped by FHIR formatting)")
}

func TestTransform_ToDateTime_TwelveHourClock(t *testing.T) {
	sm := mapWithTransform("toDateTime", []structuremap.Parameter{
		{ValueID: "v"}, {ValueString: "yyyy-MM-dd hh:mm a"},
	}, true)
	got, err := NewEngine(nil).Transform(context.Background(), sm, map[string]any{"value": "2026-05-30 09:30 PM"})
	require.NoError(t, err)
	out, _ := got.(map[string]any)["out"].(string)
	assert.Contains(t, out, "21:30", "PM must roll the hour to 21, got %q", out)
}
