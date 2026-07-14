package hl7v2

import "testing"

// FuzzParse checks that Parse never panics on arbitrary input, and that any
// message it accepts serializes back to ER7 without panicking.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"MSH|^~\\&|APP|FAC|APP2|FAC2|20240101||ADT^A01|1|P|2.5\rPID|1||123^^^HOSP||DOE^JOHN",
		"H|^~\\&||LAB|||ORU|||1||P|H2.1|20240101\rP|1|X||Y|DOE^JANE|||F",
		"MSH|^~\\&|",
		"not an hl7 message",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		target, err := Parse(text)
		if err != nil {
			return
		}
		_ = ToER7(target)
	})
}
