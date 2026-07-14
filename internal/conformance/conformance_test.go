//go:build conformance

// Package conformance_test is a black-box FHIR conformance harness. It speaks
// only HTTP to a live fhir-map server (no internal domain packages, no pgx),
// exercises a CRUD + operation coverage matrix across the R4 and R5 trees, and
// writes every response body to disk. A dedicated CI step then runs the HL7
// Validator over the captured FHIR resources.
//
// Run: go test -v -tags conformance -count=1 ./internal/conformance/
// Env: CONFORMANCE_BASE_URL (default http://localhost:8080)
//
//	CONFORMANCE_OUT_DIR  (default build/conformance-out, relative to repo root)
package conformance_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	baseURL  string
	outDir   string
	repoRoot string
	client   = &http.Client{Timeout: 60 * time.Second}
)

func TestMain(m *testing.M) {
	baseURL = strings.TrimRight(envOr("CONFORMANCE_BASE_URL", "http://localhost:8080"), "/")

	root, err := findRepoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance: cannot locate repo root:", err)
		os.Exit(1)
	}
	repoRoot = root

	outDir = envOr("CONFORMANCE_OUT_DIR", filepath.Join("build", "conformance-out"))
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(repoRoot, outDir)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "conformance: cannot create out dir:", err)
		os.Exit(1)
	}
	fmt.Printf("conformance: base=%s out=%s\n", baseURL, outDir)

	os.Exit(m.Run())
}

// tree maps a version token to its URL path segment.
func tree(version string) string {
	switch version {
	case "r4":
		return "/fhir/R4"
	default:
		return "/fhir/R5"
	}
}

func TestConformance(t *testing.T) {
	versions := []string{"r5", "r4"}

	for _, v := range versions {
		v := v
		t.Run("ConceptMap/"+v, func(t *testing.T) { runConceptMap(t, v) })
		t.Run("StructureMap/"+v, func(t *testing.T) { runStructureMap(t, v) })
		t.Run("StructureDefinition/"+v, func(t *testing.T) { runStructureDefinition(t, v) })
		t.Run("metadata/"+v, func(t *testing.T) { runMetadata(t, v) })
		t.Run("OperationOutcome/"+v, func(t *testing.T) { runErrorPaths(t, v) })
	}

	// Cross-version rejection: an R5-only field posted to the R4 tree must be
	// rejected (extends the existing W_Conformance/ Bruno coverage).
	t.Run("cross-version-rejection", runCrossVersionRejection)
}

func runConceptMap(t *testing.T, v string) {
	const model, dir = "ConceptMap", "conceptmap"
	fixture := loadFixture(t, v, dir)
	id := resourceID(t, fixture)
	prefix := tree(v) + "/ConceptMap"

	// CRUD: PUT (upsert, stable id → idempotent), read, search, history.
	t.Run("put-create", func(t *testing.T) {
		body := capture(t, http.MethodPut, prefix+"/"+id, fixture, 200, 201)
		writeResource(t, dir, v, "put-create", 1, body)
	})
	t.Run("read", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"/"+id, nil, 200)
		writeResource(t, dir, v, "read", 1, body)
	})
	t.Run("search", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"?url="+queryEscape(canonicalURL(t, fixture)), nil, 200)
		writeResource(t, dir, v, "search", 1, body)
	})
	t.Run("history", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"/"+id+"/_history", nil, 200)
		writeResource(t, dir, v, "history", 1, body)
	})

	// $translate — happy path against the J18.9 → SNOMED mapping in the fixture.
	t.Run("translate", func(t *testing.T) {
		req := translateParams(canonicalURL(t, fixture), "http://hl7.org/fhir/sid/icd-10", "J18.9")
		body := capture(t, http.MethodPost, prefix+"/$translate", req, 200)
		writeResource(t, dir, v, "translate", 1, body)
		if got := paramBool(body, "result"); got != "true" {
			t.Errorf("$translate result = %q, want true", got)
		}
	})

	// $translate-batch — one hit + one miss, expect two per-probe entries.
	t.Run("translate-batch", func(t *testing.T) {
		req := translateBatchParams(canonicalURL(t, fixture),
			[2]string{"J18.9", "http://hl7.org/fhir/sid/icd-10"},
			[2]string{"NO-SUCH-CODE", "http://hl7.org/fhir/sid/icd-10"})
		body := capture(t, http.MethodPost, prefix+"/$translate-batch", req, 200)
		writeResource(t, dir, v, "translate-batch", 1, body)
		if n := countParams(body, "translate"); n != 2 {
			t.Errorf("$translate-batch produced %d translate entries, want 2", n)
		}
	})
}

func runStructureMap(t *testing.T, v string) {
	const dir = "structuremap"
	fixture := loadFixture(t, v, dir)
	id := resourceID(t, fixture)
	prefix := tree(v) + "/StructureMap"

	t.Run("put-create", func(t *testing.T) {
		body := capture(t, http.MethodPut, prefix+"/"+id, fixture, 200, 201)
		writeResource(t, dir, v, "put-create", 1, body)
	})
	t.Run("read", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"/"+id, nil, 200)
		writeResource(t, dir, v, "read", 1, body)
	})
	t.Run("search", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"?url="+queryEscape(canonicalURL(t, fixture)), nil, 200)
		writeResource(t, dir, v, "search", 1, body)
	})

	// $transform. R5 accepts sourceMap+content and returns the raw transformed
	// object (NOT a FHIR resource — captured to a .txt sidecar, never fed to the
	// validator). R4 must reject the R5-only sourceMap parameter with a 400
	// OperationOutcome, which IS a FHIR resource and is validated.
	t.Run("transform", func(t *testing.T) {
		req := transformParams()
		if v == "r4" {
			body := capture(t, http.MethodPost, prefix+"/$transform", req, 400)
			writeResource(t, dir, v, "transform-reject", 1, body)
			return
		}
		body := capture(t, http.MethodPost, prefix+"/$transform", req, 200)
		writeRaw(t, dir, v, "transform-output", 1, body)
	})
}

func runStructureDefinition(t *testing.T, v string) {
	const dir = "structuredefinition"
	fixture := loadFixture(t, v, dir)
	id := resourceID(t, fixture)
	prefix := tree(v) + "/StructureDefinition"

	t.Run("put-create", func(t *testing.T) {
		body := capture(t, http.MethodPut, prefix+"/"+id, fixture, 200, 201)
		writeResource(t, dir, v, "put-create", 1, body)
	})
	t.Run("read", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"/"+id, nil, 200)
		writeResource(t, dir, v, "read", 1, body)
	})
	t.Run("search", func(t *testing.T) {
		body := capture(t, http.MethodGet, prefix+"?url="+queryEscape(canonicalURL(t, fixture)), nil, 200)
		writeResource(t, dir, v, "search", 1, body)
	})
}

func runMetadata(t *testing.T, v string) {
	body := capture(t, http.MethodGet, tree(v)+"/metadata", nil, 200)
	writeResource(t, "metadata", v, "capability", 1, body)
	if rt := resourceType(body); rt != "CapabilityStatement" {
		t.Errorf("metadata resourceType = %q, want CapabilityStatement", rt)
	}
}

// runErrorPaths posts an intentionally-invalid body for each model and asserts
// a 4xx OperationOutcome. Validation failures map to 422 on this server; the
// OperationOutcome body is captured and validated by CI.
func runErrorPaths(t *testing.T, v string) {
	cases := []struct {
		model, dir, body string
	}{
		{"ConceptMap", "conceptmap", `{"resourceType":"ConceptMap"}`},
		{"StructureMap", "structuremap", `{"resourceType":"StructureMap"}`},
		{"StructureDefinition", "structuredefinition", `{"resourceType":"StructureDefinition"}`},
	}
	for _, c := range cases {
		c := c
		t.Run(c.model, func(t *testing.T) {
			status, body := do(t, http.MethodPost, tree(v)+"/"+c.model, []byte(c.body))
			if status < 400 || status >= 500 {
				t.Errorf("invalid %s POST status = %d, want a 4xx", c.model, status)
			}
			if rt := resourceType(body); rt != "OperationOutcome" {
				t.Errorf("invalid %s POST body resourceType = %q, want OperationOutcome", c.model, rt)
			}
			writeResource(t, c.dir, v, "error-invalid", 1, body)
		})
	}
}

// runCrossVersionRejection posts a ConceptMap carrying the R5-only
// versionAlgorithmString field to the R4 tree; the server must reject it with a
// 400 not-supported OperationOutcome.
func runCrossVersionRejection(t *testing.T) {
	body := `{"resourceType":"ConceptMap","url":"http://example.org/fhir/ConceptMap/xver","status":"active","versionAlgorithmString":"semver"}`
	status, resp := do(t, http.MethodPost, "/fhir/R4/ConceptMap", []byte(body))
	if status != 400 {
		t.Errorf("R4 cross-version POST status = %d, want 400", status)
	}
	if rt := resourceType(resp); rt != "OperationOutcome" {
		t.Errorf("R4 cross-version body resourceType = %q, want OperationOutcome", rt)
	}
	writeResource(t, "conceptmap", "r4", "xversion-reject", 1, resp)
}

// do performs an HTTP call and returns the status code and raw response body.
func do(t *testing.T, method, path string, body []byte) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+path, rdr)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	req.Header.Set("Accept", "application/fhir+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/fhir+json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response %s %s: %v", method, path, err)
	}
	return resp.StatusCode, respBody
}

// capture performs the call, asserts the status is one of wantStatus, and
// returns the body. Always writes the body to disk via the caller.
func capture(t *testing.T, method, path string, body []byte, wantStatus ...int) []byte {
	t.Helper()
	status, resp := do(t, method, path, body)
	if !contains(wantStatus, status) {
		t.Fatalf("%s %s: status = %d, want one of %v\nbody: %s", method, path, status, wantStatus, truncate(resp))
	}
	return resp
}

// writeResource writes a FHIR resource body. The filename embeds the version
// token (-r5- / -r4-) so the CI validator glob can select it.
func writeResource(t *testing.T, model, version, op string, seq int, body []byte) {
	t.Helper()
	dir := filepath.Join(outDir, model, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	name := fmt.Sprintf("%s-%s-%s-%d.json", model, version, op, seq)
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// writeRaw writes a non-FHIR body (e.g. a $transform output object) with a .txt
// extension so the CI validator (which globs *.json) never tries to validate it.
func writeRaw(t *testing.T, model, version, op string, seq int, body []byte) {
	t.Helper()
	dir := filepath.Join(outDir, "_raw", model, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	name := fmt.Sprintf("%s-%s-%s-%d.txt", model, version, op, seq)
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func loadFixture(t *testing.T, version, model string) []byte {
	t.Helper()
	path := filepath.Join(repoRoot, "testdata", "conformance", version, model, model+"-"+version+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("load fixture %s: %v", path, err)
	}
	return b
}

func resourceID(t *testing.T, body []byte) string {
	t.Helper()
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ID == "" {
		t.Fatalf("fixture has no id: %v", err)
	}
	return r.ID
}

func canonicalURL(t *testing.T, body []byte) string {
	t.Helper()
	var r struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.URL == "" {
		t.Fatalf("fixture has no url: %v", err)
	}
	return r.URL
}

func resourceType(body []byte) string {
	var r struct {
		ResourceType string `json:"resourceType"`
	}
	_ = json.Unmarshal(body, &r)
	return r.ResourceType
}

// parameters models a FHIR Parameters resource for response inspection.
type parameters struct {
	Parameter []struct {
		Name         string          `json:"name"`
		ValueBoolean *bool           `json:"valueBoolean"`
		Part         json.RawMessage `json:"part"`
	} `json:"parameter"`
}

// paramBool returns the string form of the named top-level boolean parameter.
func paramBool(body []byte, name string) string {
	var p parameters
	if err := json.Unmarshal(body, &p); err != nil {
		return ""
	}
	for _, pr := range p.Parameter {
		if pr.Name == name && pr.ValueBoolean != nil {
			if *pr.ValueBoolean {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

// countParams counts top-level parameters with the given name.
func countParams(body []byte, name string) int {
	var p parameters
	if err := json.Unmarshal(body, &p); err != nil {
		return 0
	}
	n := 0
	for _, pr := range p.Parameter {
		if pr.Name == name {
			n++
		}
	}
	return n
}

func translateParams(url, system, code string) []byte {
	return mustJSON(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{"name": "url", "valueUri": url},
			map[string]any{"name": "sourceCoding", "valueCoding": map[string]any{"system": system, "code": code}},
		},
	})
}

func translateBatchParams(url string, codes ...[2]string) []byte {
	params := []any{map[string]any{"name": "url", "valueUri": url}}
	for _, c := range codes {
		params = append(params, map[string]any{
			"name": "code",
			"part": []any{
				map[string]any{"name": "code", "valueCode": c[0]},
				map[string]any{"name": "system", "valueUri": c[1]},
			},
		})
	}
	return mustJSON(map[string]any{"resourceType": "Parameters", "parameter": params})
}

// transformParams builds a $transform request using the R5 spec parameter names
// (sourceMap + content). On the R4 tree sourceMap is R5-only and is rejected.
func transformParams() []byte {
	return mustJSON(map[string]any{
		"resourceType": "Parameters",
		"parameter": []any{
			map[string]any{
				"name": "sourceMap",
				"resource": map[string]any{
					"resourceType": "StructureMap",
					"url":          "http://example.org/sm/conformance-inline",
					"name":         "ConformanceInline",
					"status":       "active",
					"group": []any{
						map[string]any{
							"name": "g",
							"input": []any{
								map[string]any{"name": "src", "mode": "source"},
								map[string]any{"name": "tgt", "mode": "target"},
							},
							"rule": []any{
								map[string]any{
									"name":   "value",
									"source": []any{map[string]any{"context": "src", "element": "value", "variable": "v"}},
									"target": []any{map[string]any{"context": "tgt", "element": "out", "transform": "copy", "parameter": []any{map[string]any{"valueId": "v"}}}},
								},
							},
						},
					},
				},
			},
			map[string]any{"name": "content", "resource": map[string]any{"value": "conformance-ok"}},
		},
	})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func truncate(b []byte) string {
	const max = 512
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// queryEscape escapes a query-parameter value without pulling in net/url for a
// single call; only the characters that appear in canonical URLs need handling.
func queryEscape(s string) string {
	r := strings.NewReplacer(" ", "%20", "|", "%7C")
	return r.Replace(s)
}
