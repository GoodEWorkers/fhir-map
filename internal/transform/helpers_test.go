package transform

import "github.com/goodeworkers/fhir-map/internal/translate"

// NewEngineWithResolver is a test-only constructor for an Engine with a translator and MapResolver.
func NewEngineWithResolver(translator translate.Translator, mr MapResolver) *Engine {
	return New(WithTranslator(translator), WithMapResolver(mr))
}
