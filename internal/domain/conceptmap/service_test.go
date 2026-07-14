package conceptmap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRepository implements Repository for unit testing the service layer.
type mockRepository struct {
	store map[string]*ConceptMap
	byURL map[string]*ConceptMap
}

func newMockRepository() *mockRepository {
	return &mockRepository{
		store: make(map[string]*ConceptMap),
		byURL: make(map[string]*ConceptMap),
	}
}

func (m *mockRepository) Create(_ context.Context, cm *ConceptMap) (*ConceptMap, error) {
	m.store[cm.ID] = cm
	if cm.URL != "" {
		m.byURL[cm.URL] = cm
	}
	return cm, nil
}

func (m *mockRepository) Read(_ context.Context, id string) (*ConceptMap, error) {
	cm, ok := m.store[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cm, nil
}

func (m *mockRepository) Update(_ context.Context, id string, cm *ConceptMap) (*ConceptMap, error) {
	if _, ok := m.store[id]; !ok {
		return nil, ErrNotFound
	}
	cm.ID = id
	m.store[id] = cm
	return cm, nil
}

func (m *mockRepository) Delete(_ context.Context, id string) error {
	if _, ok := m.store[id]; !ok {
		return ErrNotFound
	}
	delete(m.store, id)
	return nil
}

func (m *mockRepository) Search(_ context.Context, params SearchParams) (*SearchResult, error) {
	var results []ConceptMap
	for _, cm := range m.store {
		if params.Status != "" && cm.Status != params.Status {
			continue
		}
		if params.URL != "" && cm.URL != params.URL {
			continue
		}
		results = append(results, *cm)
	}
	return &SearchResult{ConceptMaps: results, Total: len(results)}, nil
}

func (m *mockRepository) FindByURL(_ context.Context, url string, _ string) (*ConceptMap, error) {
	cm, ok := m.byURL[url]
	if !ok {
		return nil, ErrNotFound
	}
	return cm, nil
}

func (m *mockRepository) FindBySourceScope(_ context.Context, scope string) (*ConceptMap, error) {
	for _, cm := range m.store {
		if cm.SourceScopeURI == scope || cm.SourceScopeCanonical == scope {
			return cm, nil
		}
	}
	return nil, ErrNotFound
}

func TestService_Create_Valid(t *testing.T) {
	svc := NewService(newMockRepository())

	cm := &ConceptMap{
		Status: "draft",
		Name:   "TestMap",
		Group: []Group{
			{
				Source: "http://source.system",
				Target: "http://target.system",
				Element: []Element{
					{
						Code: "A",
						Target: []Target{
							{Code: "B", Relationship: "equivalent"},
						},
					},
				},
			},
		},
	}

	result, err := svc.Create(context.Background(), cm)
	require.NoError(t, err)
	assert.NotEmpty(t, result.ID)
	assert.Equal(t, "ConceptMap", result.ResourceType)
	assert.Equal(t, "1", result.Meta.VersionID)
	assert.NotEmpty(t, result.Meta.LastUpdated)
}

func TestService_Create_InvalidStatus(t *testing.T) {
	svc := NewService(newMockRepository())

	cm := &ConceptMap{
		Status: "invalid-status",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}

	_, err := svc.Create(context.Background(), cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnprocessable)
}

func TestService_Create_MissingStatus(t *testing.T) {
	svc := NewService(newMockRepository())

	cm := &ConceptMap{
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}

	_, err := svc.Create(context.Background(), cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnprocessable)
}

func TestService_Create_InvalidRelationship(t *testing.T) {
	svc := NewService(newMockRepository())

	cm := &ConceptMap{
		Status: "draft",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "wrong"}}}}},
		},
	}

	_, err := svc.Create(context.Background(), cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnprocessable)
}

func TestService_Read_Found(t *testing.T) {
	repo := newMockRepository()
	svc := NewService(repo)

	cm := &ConceptMap{
		Status: "active",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}
	created, _ := svc.Create(context.Background(), cm)

	result, err := svc.Read(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, result.ID)
}

func TestService_Read_NotFound(t *testing.T) {
	svc := NewService(newMockRepository())

	_, err := svc.Read(context.Background(), "nonexistent")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_Read_EmptyID(t *testing.T) {
	svc := NewService(newMockRepository())

	_, err := svc.Read(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestService_Update_Success(t *testing.T) {
	repo := newMockRepository()
	svc := NewService(repo)

	cm := &ConceptMap{
		Status: "draft",
		Name:   "Original",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}
	created, _ := svc.Create(context.Background(), cm)

	updated := &ConceptMap{
		Status: "active",
		Name:   "Updated",
		Group: []Group{
			{Element: []Element{{Code: "C", Target: []Target{{Code: "D", Relationship: "equivalent"}}}}},
		},
	}

	result, err := svc.Update(context.Background(), created.ID, updated)
	require.NoError(t, err)
	assert.Equal(t, "active", result.Status)
	assert.Equal(t, "Updated", result.Name)
}

func TestService_Update_NotFound(t *testing.T) {
	svc := NewService(newMockRepository())

	cm := &ConceptMap{
		Status: "draft",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}

	_, err := svc.Update(context.Background(), "nonexistent", cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_Delete_Success(t *testing.T) {
	repo := newMockRepository()
	svc := NewService(repo)

	cm := &ConceptMap{
		Status: "draft",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}
	created, _ := svc.Create(context.Background(), cm)

	err := svc.Delete(context.Background(), created.ID)
	require.NoError(t, err)

	_, err = svc.Read(context.Background(), created.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_Delete_NotFound(t *testing.T) {
	svc := NewService(newMockRepository())

	err := svc.Delete(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_Search_ByStatus(t *testing.T) {
	repo := newMockRepository()
	svc := NewService(repo)

	cm1 := &ConceptMap{Status: "draft", Group: []Group{{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}}}}
	cm2 := &ConceptMap{Status: "active", Group: []Group{{Element: []Element{{Code: "C", Target: []Target{{Code: "D", Relationship: "equivalent"}}}}}}}
	svc.Create(context.Background(), cm1)
	svc.Create(context.Background(), cm2)

	result, err := svc.Search(context.Background(), SearchParams{Status: "active"})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Total)
	assert.Equal(t, "active", result.ConceptMaps[0].Status)
}

func TestService_Search_DefaultPagination(t *testing.T) {
	svc := NewService(newMockRepository())

	result, err := svc.Search(context.Background(), SearchParams{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.Total)
}

func TestService_Search_MaxCount(t *testing.T) {
	svc := NewService(newMockRepository())

	// Count is capped at 1000
	result, err := svc.Search(context.Background(), SearchParams{Count: 5000})
	require.NoError(t, err)
	assert.NotNil(t, result)
}

func TestService_FindByURL(t *testing.T) {
	repo := newMockRepository()
	svc := NewService(repo)

	cm := &ConceptMap{
		URL:    "http://example.org/ConceptMap/test",
		Status: "active",
		Group: []Group{
			{Element: []Element{{Code: "A", Target: []Target{{Code: "B", Relationship: "equivalent"}}}}},
		},
	}
	svc.Create(context.Background(), cm)

	result, err := svc.FindByURL(context.Background(), "http://example.org/ConceptMap/test", "")
	require.NoError(t, err)
	assert.Equal(t, "http://example.org/ConceptMap/test", result.URL)
}

func TestService_FindByURL_NotFound(t *testing.T) {
	svc := NewService(newMockRepository())

	_, err := svc.FindByURL(context.Background(), "http://nonexistent", "")
	assert.ErrorIs(t, err, ErrNotFound)
}
