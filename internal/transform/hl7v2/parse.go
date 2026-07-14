// Package hl7v2 implements a minimal source adapter for HL7v2 / HPRIM
// pipe-delimited messages.
//
// FHIR doesn't have a built-in HL7v2 source type — HAPI exposes one via
// its `hapi-fhir-converter-r4-hl7v2` module, and the legacy Postman test
// suite supplied with this repo relies on that adapter to transform
// HL7v2/HPRIM messages packaged inside a `Binary` FHIR resource.
//
// We parse the Binary's base64 payload, split into segments by line
// terminator, then split each segment into fields by the pipe (`|`)
// delimiter. The resulting `Message` is a JSON-shaped map suitable for
// the FHIRPath evaluator's navigate() function.
//
// Segment-field paths are exposed as keys like `H-2` (= field 2 of the
// first H segment). Multi-segment messages (multiple OBR, OBX) are
// exposed as a single map entry per segment name.
package hl7v2

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// IsHL7v2Binary returns true if the given JSON-shaped value looks like a
// FHIR Binary resource carrying HL7v2 / HPRIM content. The check is
// content-driven: we decode the data, check that the first segment is a
// known HL7v2 / HPRIM header (`MSH`, `H`, `BHS`, `FHS`).
func IsHL7v2Binary(v any) bool {
	bin, ok := v.(map[string]any)
	if !ok {
		return false
	}
	if rt, _ := bin["resourceType"].(string); rt != "Binary" {
		return false
	}
	data, _ := bin["data"].(string)
	if data == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return false
	}
	first := strings.SplitN(string(decoded), "|", 2)
	if len(first) == 0 {
		return false
	}
	head := strings.TrimSpace(first[0])
	switch head {
	case "MSH", "H", "BHS", "FHS":
		return true
	}
	return false
}

// AdaptBinary converts an HL7v2 Binary into a JSON-shaped map that
// FHIRPath navigation can consume with `SEG-N` paths.
//
// Resulting structure:
//
//	{
//	  "_segments": [["H","^~\\&","",...], ["P", ...], ...],   // raw lines
//	  "H":   ["H","^~\\&","","LMX5",...],
//	  "H-1": "^~\\&",       // field 1 of first H segment (1-based)
//	  "H-2": "",            // field 2
//	  ...
//	  "OBR": [...first OBR line...],
//	  "OBR-1": "0001",
//	}
//
// For repeating segments (e.g. multiple OBR), only the first occurrence
// is currently keyed by the bare segment name.
func AdaptBinary(v any) (map[string]any, error) {
	bin, _ := v.(map[string]any)
	data, _ := bin["data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("decode Binary.data: %w", err)
	}
	return Parse(string(decoded))
}

// explicitNull is the HL7v2 two-double-quote sentinel meaning "delete / set to
// null" (distinct from an absent field). nullSuffix tags the inert sidecar key
// (SEG-N#null) that records it; `#` keeps the key out of the segment-name and
// SEG-N field-key namespaces, so the serializer/navigator ignore it.
const (
	explicitNull = `""`
	nullSuffix   = "#null"
)

// Delims is the delimiter set for one message, discovered from its header
// (MSH-1/MSH-2 or the H segment) rather than hardcoded. Conventional HL7v2
// headers yield defaultDelims, so default-delimited messages tokenize exactly
// as the legacy hardcoded splits did. Threaded through tokenization per spec
// §5.2/§14 — never a package-level constant.
type Delims struct {
	Field        byte
	Component    byte
	Repetition   byte
	Escape       byte
	Subcomponent byte
	Truncation   byte // 0 when absent (pre-2.7 messages omit the 5th encoding char)
}

// defaultDelims is the HL7v2 convention `|^~\&` (no truncation char). It
// reproduces the previously-hardcoded field/component/subcomponent splits
// byte-for-byte.
func defaultDelims() Delims {
	return Delims{Field: '|', Component: '^', Repetition: '~', Escape: '\\', Subcomponent: '&'}
}

// discover reads the message header to determine the delimiter set. It returns
// defaultDelims for conventional `^~\&` headers so the common case is
// byte-identical to the legacy parser; non-default encodings are extracted
// per HL7v2 spec (component, repetition, escape, subcomponent, optional truncation).
func discover(text string) Delims {
	d := defaultDelims()
	line := firstSegment(text)
	idLen := 0
	switch {
	case strings.HasPrefix(line, "MSH"), strings.HasPrefix(line, "FHS"), strings.HasPrefix(line, "BHS"):
		idLen = 3
	case strings.HasPrefix(line, "H"):
		idLen = 1
	default:
		return d // not a delimiter-declaring header
	}
	if len(line) <= idLen {
		return d
	}
	d.Field = line[idLen]
	enc := line[idLen+1:]
	if i := strings.IndexByte(enc, d.Field); i >= 0 {
		enc = enc[:i] // encoding chars run up to the next field separator
	}
	set := func(dst *byte, i int) {
		if i < len(enc) {
			*dst = enc[i]
		}
	}
	set(&d.Component, 0)
	set(&d.Repetition, 1)
	set(&d.Escape, 2)
	set(&d.Subcomponent, 3)
	set(&d.Truncation, 4)
	return d
}

// firstSegment returns the first non-blank, CR/LF-normalized line of text.
func firstSegment(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// emitFieldKeys writes the navigable keys for one HL7v2 field at path `key`
// (e.g. "PID-5"), splitting on the message's discovered delimiters:
//
//	SEG-N        the raw field value
//	SEG-N-C      component C (1-based), splitting the field on d.Component
//	SEG-N-C-S    subcomponent S (1-based), splitting component C on d.Subcomponent
//
// Empty values are absent — no key is emitted — so `$this.exists()` on an
// empty field is false (matching FHIRPath spec).
//
// Leaf values are unescaped (§5.6) after splitting, except when raw is set,
// which preserves the encoding-characters field (MSH-1 / H-1) as an opaque literal.
//
// Repeating fields are split on d.Repetition and emitted as []any;
// single-repetition fields stay scalar strings (byte-identical to legacy parser).
// The encoding field is never repetition-split.
func emitFieldKeys(out map[string]any, key, value string, d Delims, raw bool) {
	if value == "" {
		return // absent — no value, "leave unchanged", .exists() = false
	}
	if !raw && value == explicitNull {
		out[key+nullSuffix] = true // explicit-null ("") — distinct from absent
		return
	}
	if !raw {
		if reps := strings.Split(value, string(d.Repetition)); len(reps) > 1 {
			list := make([]any, len(reps))
			for i, r := range reps {
				list[i] = unescape(r, d)
			}
			out[key] = list
			emitComponents(out, key, reps[0], d, raw) // components off the first rep
			return
		}
	}
	out[key] = leafValue(value, d, raw)
	emitComponents(out, key, value, d, raw)
}

// emitComponents writes the SEG-N-C / SEG-N-C-S component and subcomponent keys
// for one (single-repetition) field value, unescaping each leaf unless raw.
func emitComponents(out map[string]any, key, value string, d Delims, raw bool) {
	comps := strings.Split(value, string(d.Component))
	for ci, comp := range comps {
		if comp == "" {
			continue
		}
		ckey := key + "-" + strconv.Itoa(ci+1)
		if !raw && comp == explicitNull {
			out[ckey+nullSuffix] = true
			continue
		}
		out[ckey] = leafValue(comp, d, raw)
		if subs := strings.Split(comp, string(d.Subcomponent)); len(subs) > 1 {
			for si, sub := range subs {
				switch {
				case sub == "":
					// absent subcomponent — no key
				case !raw && sub == explicitNull:
					out[ckey+"-"+strconv.Itoa(si+1)+nullSuffix] = true
				default:
					out[ckey+"-"+strconv.Itoa(si+1)] = leafValue(sub, d, raw)
				}
			}
		}
	}
}

// leafValue unescapes a tokenized leaf unless raw is set (the opaque encoding
// field).
func leafValue(s string, d Delims, raw bool) string {
	if raw {
		return s
	}
	return unescape(s, d)
}

// Parse turns HL7v2 message text into the navigable map described on
// AdaptBinary. Exposed for tests and for callers that already have the decoded
// text (e.g. a non-base64-encoded source).
//
// Segments of the same name are exposed as a list-of-rows under the bare-name
// key, uniformly for single and multiple occurrences — this lets FHIRPath rules
// iterate per-segment naturally. Field-indexed keys (`OBR-1`) preserve
// first-occurrence semantics for backward compatibility.
func Parse(text string) (map[string]any, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	d := discover(text)
	fieldSep := string(d.Field)

	out := map[string]any{}
	byName := map[string][]any{}
	var segments []any
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, fieldSep)
		if len(fields) == 0 {
			continue
		}
		seg := fields[0]
		// The encoding-characters field (index 1 of a header segment)
		// is opaque: never unescaped or repetition-split.
		isHeader := seg == "MSH" || seg == "FHS" || seg == "BHS" || seg == "H"

		rawRow := make([]any, len(fields))
		for i, f := range fields {
			rawRow[i] = f
		}
		segments = append(segments, rawRow)

		// Per-row navigable object keyed SEG-N / SEG-N-C[-S] so FHIRPath rules
		// can iterate `source.OBX as obs` and access `obs.OBX-3-1`.
		row := map[string]any{}
		for i := 1; i < len(fields); i++ {
			emitFieldKeys(row, seg+"-"+strconv.Itoa(i), fields[i], d, isHeader && i == 1)
		}
		byName[seg] = append(byName[seg], row)

		// Top-level segment fields on first occurrence for direct paths like `source.MSH-7`.
		if _, exists := out[seg+"-1"]; !exists && len(fields) > 1 {
			for i := 1; i < len(fields); i++ {
				emitFieldKeys(out, seg+"-"+strconv.Itoa(i), fields[i], d, isHeader && i == 1)
			}
		}
	}
	for name, rows := range byName {
		out[name] = rows
	}
	out["_segments"] = segments
	return out, nil
}
