package conceptmap

import (
	"context"
	"errors"
	"testing"

	"github.com/goodeworkers/fhir-map/pkg/fhir"
)

// Extra tests for argument-guard branches and Resource accessors.

func TestService_Delete_EmptyID(t *testing.T) {
	svc := NewService(newMockRepository())
	if err := svc.Delete(context.Background(), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id should be ErrInvalidInput, got %v", err)
	}
}

func TestService_Update_EmptyID(t *testing.T) {
	svc := NewService(newMockRepository())
	if _, err := svc.Update(context.Background(), "", &ConceptMap{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id should be ErrInvalidInput, got %v", err)
	}
}

func TestService_FindByURL_EmptyURL(t *testing.T) {
	svc := NewService(newMockRepository())
	if _, err := svc.FindByURL(context.Background(), "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty url should be ErrInvalidInput, got %v", err)
	}
}

func TestConceptMap_ResourceAccessors(t *testing.T) {
	cm := &ConceptMap{}
	cm.SetID("abc")
	if cm.GetID() != "abc" {
		t.Fatalf("SetID/GetID round-trip failed, got %q", cm.GetID())
	}
	m := &fhir.Meta{VersionID: "3"}
	cm.SetMeta(m)
	if cm.GetMeta() == nil || cm.GetMeta().VersionID != "3" {
		t.Fatalf("SetMeta/GetMeta round-trip failed, got %+v", cm.GetMeta())
	}
}
