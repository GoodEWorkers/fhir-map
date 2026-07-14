package resolver

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
)

// mockRepo implements structuredefinition.Repository with a configurable store.
type mockRepo struct {
	findByURL   func(ctx context.Context, url, version string) (*structuredefinition.StructureDefinition, error)
	findByURLCt int64 // call count, accessed atomically
}

func (m *mockRepo) Create(_ context.Context, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRepo) Read(_ context.Context, id string) (*structuredefinition.StructureDefinition, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRepo) Update(_ context.Context, id string, sd *structuredefinition.StructureDefinition) (*structuredefinition.StructureDefinition, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRepo) Delete(_ context.Context, id string) error { return errors.New("not implemented") }
func (m *mockRepo) Search(_ context.Context, params structuredefinition.SearchParams) (*structuredefinition.SearchResult, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRepo) FindByURL(ctx context.Context, url, version string) (*structuredefinition.StructureDefinition, error) {
	atomic.AddInt64(&m.findByURLCt, 1)
	if m.findByURL != nil {
		return m.findByURL(ctx, url, version)
	}
	return nil, structuredefinition.ErrNotFound
}
func (m *mockRepo) History(_ context.Context, id string) ([]structuredefinition.HistoryEntry, error) {
	return nil, errors.New("not implemented")
}
func (m *mockRepo) ReadVersion(_ context.Context, id string, versionID int) (*structuredefinition.StructureDefinition, error) {
	return nil, errors.New("not implemented")
}

var _ structuredefinition.Repository = (*mockRepo)(nil)

func TestResolver_ShortNameFastPath(t *testing.T) {
	repo := &mockRepo{}
	r := NewResolver(repo)

	result, err := r.ResolveType(context.Background(), "Patient")
	require.NoError(t, err)
	assert.Equal(t, "Patient", result)
	assert.Equal(t, int64(0), repo.findByURLCt, "short-name fast path must not hit the DB")
}

func TestResolver_HL7CanonicalURL_Patient(t *testing.T) {
	r := NewResolver(nil) // no repo needed; hl7base covers Patient
	result, err := r.ResolveType(context.Background(), "http://hl7.org/fhir/StructureDefinition/Patient")
	require.NoError(t, err)
	assert.Equal(t, "Patient", result)
}

func TestResolver_HL7PrimitiveURL_String(t *testing.T) {
	r := NewResolver(nil)
	result, err := r.ResolveType(context.Background(), "http://hl7.org/fhir/StructureDefinition/string")
	require.NoError(t, err)
	assert.Equal(t, "string", result)
}

func TestResolver_HL7PrimitiveURLs_AllPrimitives(t *testing.T) {
	r := NewResolver(nil)
	primitives := []string{
		"boolean", "integer", "decimal", "string", "code", "uri", "dateTime",
	}
	for _, p := range primitives {
		t.Run(p, func(t *testing.T) {
			url := "http://hl7.org/fhir/StructureDefinition/" + p
			result, err := r.ResolveType(context.Background(), url)
			require.NoError(t, err)
			assert.Equal(t, p, result)
		})
	}
}

func TestResolver_ProfileChainsToBase_ViaBaseDefinition(t *testing.T) {
	profileURL := "http://my-hospital.org/fhir/StructureDefinition/MyPatientProfile"
	repo := &mockRepo{
		findByURL: func(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
			if url == profileURL {
				return &structuredefinition.StructureDefinition{
					URL:            profileURL,
					Type:           "Patient",
					Kind:           "resource",
					BaseDefinition: "http://hl7.org/fhir/StructureDefinition/Patient",
					Derivation:     "constraint",
				}, nil
			}
			return nil, structuredefinition.ErrNotFound
		},
	}
	r := NewResolver(repo)
	result, err := r.ResolveType(context.Background(), profileURL)
	require.NoError(t, err)
	assert.Equal(t, "Patient", result)
}

func TestResolver_UnknownURL_ReturnsErrCanonicalResolution(t *testing.T) {
	unknownURL := "http://example.org/fhir/StructureDefinition/Unknown"
	repo := &mockRepo{} // returns ErrNotFound for everything
	r := NewResolver(repo)

	_, err := r.ResolveType(context.Background(), unknownURL)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCanonicalResolution), "expected ErrCanonicalResolution, got: %v", err)
	assert.Contains(t, err.Error(), unknownURL, "error must name the unresolved URL")
}

func TestResolver_ChainExceedsCap_ReturnsErrCanonicalResolution(t *testing.T) {
	const depth = 9
	baseURL := "http://example.org/chain/"
	repo := &mockRepo{
		findByURL: func(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
			for i := 0; i < depth; i++ {
				if url == fmt.Sprintf("%s%d", baseURL, i) {
					return &structuredefinition.StructureDefinition{
						URL:            url,
						BaseDefinition: fmt.Sprintf("%s%d", baseURL, i+1),
					}, nil
				}
			}
			return nil, structuredefinition.ErrNotFound
		},
	}
	r := NewResolver(repo)
	_, err := r.ResolveType(context.Background(), baseURL+"0")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCanonicalResolution))
	assert.Contains(t, err.Error(), "hops", "error must mention the hop cap")
}

func TestResolver_Cycle_ReturnsErrCanonicalResolution(t *testing.T) {
	urlA := "http://example.org/sd/A"
	urlB := "http://example.org/sd/B"
	repo := &mockRepo{
		findByURL: func(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
			switch url {
			case urlA:
				return &structuredefinition.StructureDefinition{URL: urlA, BaseDefinition: urlB}, nil
			case urlB:
				return &structuredefinition.StructureDefinition{URL: urlB, BaseDefinition: urlA}, nil
			}
			return nil, structuredefinition.ErrNotFound
		},
	}
	r := NewResolver(repo)
	_, err := r.ResolveType(context.Background(), urlA)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCanonicalResolution))
	assert.Contains(t, err.Error(), "loop", "error must mention the cycle")
}

func TestResolver_NilRepo_UnknownURL_ReturnsErrCanonicalResolution(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.ResolveType(context.Background(), "http://example.org/fhir/StructureDefinition/Unknown")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCanonicalResolution))
}

func TestResolver_LeafURLType_ReturnsURLAsType(t *testing.T) {
	leafURL := "http://example.org/fhir/StructureDefinition/LeafURLType"
	urlType := "http://example.org/fhir/SomeAbstractType"
	repo := &mockRepo{
		findByURL: func(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
			if url == leafURL {
				return &structuredefinition.StructureDefinition{
					URL:            leafURL,
					Type:           urlType,
					BaseDefinition: "",
				}, nil
			}
			return nil, structuredefinition.ErrNotFound
		},
	}
	r := NewResolver(repo)
	result, err := r.ResolveType(context.Background(), leafURL)
	require.NoError(t, err)
	assert.Equal(t, urlType, result)
}

func TestResolver_Cache_OneStoreHitPer1000Lookups(t *testing.T) {
	obsURL := "http://hl7.org/fhir/StructureDefinition/Observation"
	profileURL := "http://example.org/fhir/StructureDefinition/MyObs"
	var callCount int64
	repo := &mockRepo{
		findByURL: func(_ context.Context, url, _ string) (*structuredefinition.StructureDefinition, error) {
			atomic.AddInt64(&callCount, 1)
			if url == profileURL {
				return &structuredefinition.StructureDefinition{
					URL:  profileURL,
					Type: "Observation",
				}, nil
			}
			return nil, structuredefinition.ErrNotFound
		},
	}
	r := NewResolver(repo)

	for i := 0; i < 1000; i++ {
		result, err := r.ResolveType(context.Background(), obsURL)
		require.NoError(t, err)
		assert.Equal(t, "Observation", result)
	}
	assert.Equal(t, int64(0), atomic.LoadInt64(&callCount), "HL7 base URL must never hit the repo")

	atomic.StoreInt64(&callCount, 0)
	for i := 0; i < 1000; i++ {
		result, err := r.ResolveType(context.Background(), profileURL)
		require.NoError(t, err)
		assert.Equal(t, "Observation", result)
	}
	assert.Equal(t, int64(1), atomic.LoadInt64(&callCount), "profile URL must call FindByURL exactly once (cache hit on subsequent calls)")
}
