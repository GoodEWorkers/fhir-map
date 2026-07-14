package transform

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuremap"
)

func loadGoldenMap(t *testing.T, name string) *structuremap.StructureMap {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hl7v2_lab_golden", name+".json"))
	require.NoError(t, err)
	sm := &structuremap.StructureMap{}
	require.NoError(t, json.Unmarshal(b, sm))
	return sm
}

func TestGolden_Lab_ORU_to_FHIR(t *testing.T) {
	imports := []string{
		"LabA-MSH-to-LabReport", "LabA-OBX-to-LabReport-Observation",
		"LabA-ORC-to-LabReport-ServiceRequest", "LabA-PID-to-LabReport-Patient",
		"LabA-OBR-to-LabReport",
	}
	resolver := &fakeResolver{maps: map[string]*structuremap.StructureMap{}}
	for _, name := range imports {
		m := loadGoldenMap(t, name)
		resolver.maps[m.URL] = m
	}
	oru := loadGoldenMap(t, "LabA-ORU-to-LabReport")

	hl7, err := os.ReadFile(filepath.Join("testdata", "hl7v2_lab_golden", "lab_sample.hl7"))
	require.NoError(t, err)
	source := map[string]any{
		"resourceType": "Binary", "contentType": "text/x-hl7-ft",
		"data": base64.StdEncoding.EncodeToString(hl7),
	}

	// Conformance logging enabled: lenient path accepts empty/non-conformant fields with logging.
	logger, _ := errLogger()
	got, err := New(WithMapResolver(resolver), WithConformanceLogging(logger)).Transform(context.Background(), oru, source)
	require.NoError(t, err)
	bundle, ok := got.(map[string]any)
	require.True(t, ok)

	counts := map[string]int{}
	var loinc []string
	var obsMissingRT, obsWithValue int
	for _, e := range asSlice(bundle["entry"]) {
		res, _ := e.(map[string]any)["resource"].(map[string]any)
		if res == nil {
			continue
		}
		rt, _ := res["resourceType"].(string)
		counts[rt]++
		if isObservationShape(res) {
			if rt == "" {
				obsMissingRT++
			}
			if _, ok := res["valueQuantity"]; ok {
				obsWithValue++ // value[x] resolved to valueQuantity
			}
			for _, c := range codingsOf(res["code"]) {
				if c["system"] == "http://loinc.org" {
					if code := c["code"]; code != "" {
						loinc = append(loinc, code)
					}
				}
			}
		}
	}
	sort.Strings(loinc)
	t.Logf("bundle.type=%v counts=%v loinc=%v obsMissingRT=%d obsWithValue=%d", bundle["type"], counts, loinc, obsMissingRT, obsWithValue)

	assert.Zero(t, obsMissingRT, "S3: every Observation must have resourceType")
	for _, want := range []string{"1988-5", "2951-2", "2823-3", "14682-9"} {
		assert.Contains(t, loinc, want, "expected LOINC %s from the contained ConceptMap", want)
	}
	assert.GreaterOrEqual(t, obsWithValue, 4, "the 4 numeric Observations carry valueQuantity (value[x] resolved)")
	assert.Equal(t, 1, counts["Patient"], "exactly one Patient (no duplicate from PID import)")
	assert.Equal(t, 1, counts["DiagnosticReport"], "exactly one DiagnosticReport")
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
func isObservationShape(res map[string]any) bool {
	if rt, _ := res["resourceType"].(string); rt == "Observation" {
		return true
	}
	_, hasVal := res["valueQuantity"]
	_, hasCode := res["code"]
	return hasVal || hasCode
}
func codingsOf(code any) []map[string]string {
	cc, _ := code.(map[string]any)
	if cc == nil {
		return nil
	}
	var out []map[string]string
	for _, c := range asSlice(cc["coding"]) {
		m, _ := c.(map[string]any)
		sys, _ := m["system"].(string)
		cd, _ := m["code"].(string)
		out = append(out, map[string]string{"system": sys, "code": cd})
	}
	return out
}
