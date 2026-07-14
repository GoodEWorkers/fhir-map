package structuremap

import (
	"context"
	"errors"
	"testing"
)

// fakeRepo is an in-memory Repository for Service unit tests. It records the
// last call arguments so tests can assert the Service forwarded correctly.
type fakeRepo struct {
	created    *StructureMap
	updatedID  string
	deletedID  string
	searchArgs SearchParams
	findURL    string
	findVer    string
	retErr     error
}

func (f *fakeRepo) Create(_ context.Context, sm *StructureMap) (*StructureMap, error) {
	f.created = sm
	if f.retErr != nil {
		return nil, f.retErr
	}
	return sm, nil
}

func (f *fakeRepo) Read(_ context.Context, id string) (*StructureMap, error) {
	if f.retErr != nil {
		return nil, f.retErr
	}
	return &StructureMap{ID: id}, nil
}

func (f *fakeRepo) Update(_ context.Context, id string, sm *StructureMap) (*StructureMap, error) {
	f.updatedID = id
	if f.retErr != nil {
		return nil, f.retErr
	}
	return sm, nil
}

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	f.deletedID = id
	return f.retErr
}

func (f *fakeRepo) Search(_ context.Context, params SearchParams) (*SearchResult, error) {
	f.searchArgs = params
	if f.retErr != nil {
		return nil, f.retErr
	}
	return &SearchResult{Total: 0}, nil
}

func (f *fakeRepo) FindByURL(_ context.Context, url, version string) (*StructureMap, error) {
	f.findURL, f.findVer = url, version
	if f.retErr != nil {
		return nil, f.retErr
	}
	return &StructureMap{URL: url}, nil
}

func (f *fakeRepo) History(_ context.Context, _ string) ([]HistoryEntry, error) { return nil, nil }
func (f *fakeRepo) ReadVersion(_ context.Context, _ string, _ int) (*StructureMap, error) {
	return nil, nil
}

func validSM() *StructureMap {
	return &StructureMap{
		ResourceType: "StructureMap",
		URL:          "http://example.org/StructureMap/svc-test",
		Name:         "SvcTest",
		Status:       "active",
		Group: []Group{{
			Name:  "g",
			Input: []Input{{Name: "src", Mode: "source"}},
			Rule:  []Rule{{Name: "r"}},
		}},
	}
}

func TestService_Create_AssignsIDAndMeta(t *testing.T) {
	repo := &fakeRepo{}
	out, err := NewService(repo).Create(context.Background(), validSM())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ID == "" {
		t.Fatalf("Create should assign an ID")
	}
	if out.Meta == nil || out.Meta.VersionID != "1" {
		t.Fatalf("Create should set Meta version 1, got %+v", out.Meta)
	}
	if repo.created == nil {
		t.Fatalf("repo.Create was not called")
	}
}

func TestService_Create_InvalidIsUnprocessable(t *testing.T) {
	repo := &fakeRepo{}
	// Missing Name/Status/Group → strict validation fails.
	_, err := NewService(repo).Create(context.Background(), &StructureMap{URL: "http://x"})
	if !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("expected ErrUnprocessable, got %v", err)
	}
	if repo.created != nil {
		t.Fatalf("repo.Create should not be called on invalid input")
	}
}

func TestService_Read(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	if _, err := svc.Read(context.Background(), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id should be ErrInvalidInput, got %v", err)
	}
	got, err := svc.Read(context.Background(), "abc")
	if err != nil || got.ID != "abc" {
		t.Fatalf("Read(abc) = %+v, %v", got, err)
	}
}

func TestService_Update(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	if _, err := svc.Update(context.Background(), "", validSM()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id should be ErrInvalidInput, got %v", err)
	}
	if _, err := svc.Update(context.Background(), "id1", &StructureMap{URL: "http://x"}); !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("invalid body should be ErrUnprocessable, got %v", err)
	}
	out, err := svc.Update(context.Background(), "id1", validSM())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.ID != "id1" || repo.updatedID != "id1" {
		t.Fatalf("Update should set/forward id1, got out=%s repo=%s", out.ID, repo.updatedID)
	}
}

func TestService_Delete(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	if err := svc.Delete(context.Background(), ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty id should be ErrInvalidInput, got %v", err)
	}
	if err := svc.Delete(context.Background(), "id9"); err != nil {
		t.Fatalf("Delete(id9) unexpected error: %v", err)
	}
	if repo.deletedID != "id9" {
		t.Fatalf("Delete should forward id9, got %q", repo.deletedID)
	}
}

func TestService_Search_CountClamping(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)

	if _, err := svc.Search(context.Background(), SearchParams{Count: 0, Offset: -5}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.searchArgs.Count != 20 || repo.searchArgs.Offset != 0 {
		t.Fatalf("defaults wrong: count=%d offset=%d", repo.searchArgs.Count, repo.searchArgs.Offset)
	}

	if _, err := svc.Search(context.Background(), SearchParams{Count: 5000}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.searchArgs.Count != 1000 {
		t.Fatalf("count should cap at 1000, got %d", repo.searchArgs.Count)
	}
}

func TestService_FindByURL(t *testing.T) {
	repo := &fakeRepo{}
	svc := NewService(repo)
	if _, err := svc.FindByURL(context.Background(), "", ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty url should be ErrInvalidInput, got %v", err)
	}
	got, err := svc.FindByURL(context.Background(), "http://u", "1.0.0")
	if err != nil || got.URL != "http://u" {
		t.Fatalf("FindByURL = %+v, %v", got, err)
	}
	if repo.findURL != "http://u" || repo.findVer != "1.0.0" {
		t.Fatalf("FindByURL should forward url/version, got %q %q", repo.findURL, repo.findVer)
	}
}

func TestService_Create_RepoError(t *testing.T) {
	repo := &fakeRepo{retErr: errors.New("db boom")}
	_, err := NewService(repo).Create(context.Background(), validSM())
	if err == nil {
		t.Fatalf("expected repo error to propagate")
	}
}

func TestStructureMap_ResourceAccessors(t *testing.T) {
	sm := &StructureMap{}
	sm.SetID("xyz")
	if sm.GetID() != "xyz" {
		t.Fatalf("SetID/GetID round-trip failed")
	}
}
