// Package resolver maps FHIR StructureDefinition canonical URLs to their base
// FHIR type names by walking the baseDefinition chain.
package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition"
	"github.com/goodeworkers/fhir-map/internal/domain/structuredefinition/hl7base"
)

// ErrCanonicalResolution is returned when a canonical URL cannot be resolved
// to a FHIR base type. The wrapped detail names the unresolved URL.
var ErrCanonicalResolution = errors.New("canonical URL could not be resolved to a base type")

// maxBaseDefinitionHops is the hard cap on baseDefinition chain length.
// FHIR R5's deepest base chain is ~3 hops; 8 gives 5× headroom and matches
// HAPI's StructureDefinition validator convention.
const maxBaseDefinitionHops = 8

// Resolver maps a StructureDefinition canonical URL to its FHIR base type name
// by walking the baseDefinition chain.
type Resolver interface {
	// ResolveType returns the short FHIR type name for a canonical URL.
	// Short names (no "://") are returned as-is (fast path, no DB hit).
	// Returns ErrCanonicalResolution when the URL is unknown, the chain loops,
	// or the chain exceeds the depth cap (8 hops).
	ResolveType(ctx context.Context, canonicalURL string) (string, error)
}

// DefaultResolver is the standard Resolver implementation. It maintains a
// process-global sync.Map cache; resolved canonical URLs are invariant for
// a deployment lifetime.
type DefaultResolver struct {
	repo  structuredefinition.Repository
	cache sync.Map // map[string]string
}

// NewResolver creates a DefaultResolver backed by the given repository.
func NewResolver(repo structuredefinition.Repository) *DefaultResolver {
	return &DefaultResolver{repo: repo}
}

// ResolveType implements Resolver.
func (r *DefaultResolver) ResolveType(ctx context.Context, canonicalURL string) (string, error) {
	if !strings.Contains(canonicalURL, "://") {
		return canonicalURL, nil
	}

	if v, ok := r.cache.Load(canonicalURL); ok {
		return v.(string), nil
	}

	shortName, err := r.walk(ctx, canonicalURL)
	if err != nil {
		return "", err
	}

	r.cache.Store(canonicalURL, shortName)
	return shortName, nil
}

func (r *DefaultResolver) walk(ctx context.Context, startURL string) (string, error) {
	visited := make(map[string]struct{})
	currentURL := startURL
	hops := 0

	for {
		if _, seen := visited[currentURL]; seen {
			return "", fmt.Errorf("%w: chain loops at %s", ErrCanonicalResolution, currentURL)
		}
		visited[currentURL] = struct{}{}

		def := r.lookupDef(ctx, currentURL)
		if def == nil {
			return "", fmt.Errorf("%w: %s", ErrCanonicalResolution, currentURL)
		}

		// Profiled resources have short type names already (e.g. MyPatientProfile → type:"Patient").
		if def.Type != "" && !strings.Contains(def.Type, "://") {
			return def.Type, nil
		}

		if def.BaseDefinition == "" {
			if def.Type != "" {
				return def.Type, nil
			}
			return "", fmt.Errorf("%w: %s", ErrCanonicalResolution, currentURL)
		}

		hops++
		if hops > maxBaseDefinitionHops {
			return "", fmt.Errorf("%w: chain exceeds %d hops at %s", ErrCanonicalResolution, maxBaseDefinitionHops, currentURL)
		}

		currentURL = def.BaseDefinition
	}
}

// lookupDef checks the in-memory HL7 fixture first, then falls back to the repo.
// Returns nil if the URL is not found.
func (r *DefaultResolver) lookupDef(ctx context.Context, url string) *structuredefinition.StructureDefinition {
	if def, ok := hl7base.BaseTypes[url]; ok {
		return def
	}
	if r.repo == nil {
		return nil
	}
	def, err := r.repo.FindByURL(ctx, url, "")
	if err != nil {
		return nil
	}
	return def
}
