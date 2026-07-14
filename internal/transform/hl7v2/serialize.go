package hl7v2

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ToER7 renders an HL7v2 target object back into ER7 (pipe-delimited) text.
// Input: "SEG-N" keys (header fields), "SEG" arrays (repeating segments with "SEG-M" subkeys),
// or field values (strings, {"value": x} wrappers, or lists for repetitions joined with '~').
// Component keys and resourceType are ignored. Segment order follows DefaultSegmentOrder; override via ToER7With.
func ToER7(target map[string]any) string { return ToER7With(target, ER7Options{}) }

// ER7Options tunes serialization. The zero value yields standard HL7v2 output.
type ER7Options struct {
	SegmentOrder []string // emit order for known segments; others follow lexically
	FieldSep     string   // default "|"
	RepSep       string   // default "~"
	SegmentSep   string   // default "\r"
}

// DefaultSegmentOrder is the canonical emit order for segments; extend for other message types.
var DefaultSegmentOrder = []string{
	"MSH", "FHS", "BHS", "PID", "PD1", "PV1", "PV2", "ORC", "OBR", "OBX", "NTE", "SPM", "L",
}

func (o ER7Options) withDefaults() ER7Options {
	if len(o.SegmentOrder) == 0 {
		o.SegmentOrder = DefaultSegmentOrder
	}
	if o.FieldSep == "" {
		o.FieldSep = "|"
	}
	if o.RepSep == "" {
		o.RepSep = "~"
	}
	if o.SegmentSep == "" {
		o.SegmentSep = "\r"
	}
	return o
}

// ToER7With renders target using the supplied options.
func ToER7With(target map[string]any, opts ER7Options) string {
	opts = opts.withDefaults()
	occ := collectOccurrences(target)

	names := make([]string, 0, len(occ))
	for name := range occ {
		names = append(names, name)
	}
	sortByOrder(names, opts.SegmentOrder)

	var lines []string
	for _, name := range names {
		for _, fields := range occ[name] {
			lines = append(lines, renderSegment(name, fields, opts))
		}
	}
	return strings.Join(lines, opts.SegmentSep)
}

// collectOccurrences groups the target into segment occurrences. A bare "SEG"
// list (repeating segments) wins over loose "SEG-N" header keys for the same
// segment, so a segment is never emitted twice.
func collectOccurrences(target map[string]any) map[string][]map[int]any {
	out := map[string][]map[int]any{}

	for key, val := range target {
		if !isSegmentName(key) {
			continue
		}
		rows, ok := val.([]any)
		if !ok {
			continue
		}
		for _, r := range rows {
			if rm, ok := r.(map[string]any); ok {
				out[key] = append(out[key], fieldsOf(rm, key))
			}
		}
	}

	headers := map[string]map[int]any{}
	for key, val := range target {
		seg, idx, ok := splitFieldKey(key)
		if !ok {
			continue
		}
		if _, isRepeating := out[seg]; isRepeating {
			continue
		}
		if headers[seg] == nil {
			headers[seg] = map[int]any{}
		}
		headers[seg][idx] = val
	}
	for seg, fields := range headers {
		out[seg] = append(out[seg], fields)
	}
	return out
}

func fieldsOf(row map[string]any, seg string) map[int]any {
	fields := map[int]any{}
	for key, val := range row {
		if s, idx, ok := splitFieldKey(key); ok && s == seg {
			fields[idx] = val
		}
	}
	return fields
}

func renderSegment(name string, fields map[int]any, opts ER7Options) string {
	maxIdx := 0
	for i := range fields {
		if i > maxIdx {
			maxIdx = i
		}
	}
	parts := make([]string, 0, maxIdx+1)
	parts = append(parts, name)
	for i := 1; i <= maxIdx; i++ {
		parts = append(parts, renderValue(fields[i], opts))
	}
	for len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, opts.FieldSep)
}

func renderValue(v any, opts ER7Options) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		reps := make([]string, 0, len(t))
		for _, item := range t {
			reps = append(reps, renderValue(item, opts))
		}
		return strings.Join(reps, opts.RepSep)
	case map[string]any:
		if val, ok := t["value"]; ok {
			return renderValue(val, opts)
		}
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func isSegmentName(key string) bool {
	if key == "" || key == "resourceType" {
		return false
	}
	for _, r := range key {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return key[0] >= 'A' && key[0] <= 'Z'
}

func splitFieldKey(key string) (seg string, idx int, ok bool) {
	dash := strings.IndexByte(key, '-')
	if dash <= 0 || dash == len(key)-1 {
		return "", 0, false
	}
	seg, rest := key[:dash], key[dash+1:]
	if !isSegmentName(seg) {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest) // pure integer => raw field (not a component)
	if err != nil {
		return "", 0, false
	}
	return seg, n, true
}

func sortByOrder(names, order []string) {
	rank := make(map[string]int, len(order))
	for i, n := range order {
		rank[n] = i
	}
	sort.SliceStable(names, func(i, j int) bool {
		ri, oki := rank[names[i]]
		rj, okj := rank[names[j]]
		switch {
		case oki && okj:
			return ri < rj
		case oki != okj:
			return oki // ranked segments first
		default:
			return names[i] < names[j]
		}
	})
}
