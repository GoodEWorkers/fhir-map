package handler

import (
	"net/http"
	"strings"
)

// ValidationMode is the package-level validation mode used by ResourceHandler[T].
type ValidationMode int

const (
	ModeStrict  ValidationMode = iota
	ModeLenient ValidationMode = iota
)

// parseValidationMode reads ?_validate from the request using case-insensitive matching,
// mapping "lenient" to ModeLenient; anything else defaults to ModeStrict.
func parseValidationMode(r *http.Request) ValidationMode {
	if strings.EqualFold(r.URL.Query().Get("_validate"), "lenient") {
		return ModeLenient
	}
	return ModeStrict
}
