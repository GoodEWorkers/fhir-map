package handler

import "github.com/goodeworkers/fhir-map/pkg/fhir"

// Resource is the constraint that generic ResourceHandler[T] and Adapter[T] require.
type Resource interface {
	GetID() string
	SetID(string)
	GetMeta() *fhir.Meta
	SetMeta(*fhir.Meta)
}
