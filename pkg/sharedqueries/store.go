// Package sharedqueries stores SQL queries that the whole team can see, unlike
// the browser-local "Local Saved" queries kept in localStorage.
//
// The full list is held in memory and treated as the source of truth for reads;
// the backend is a write-through durability layer that gets loaded once on
// first use. That works because pgweb runs as a single task -- see the ECS
// module README in the monorepo -- so there is no second writer to reconcile
// with. If this ever runs with more than one replica, reads would need to go to
// the backend instead.
package sharedqueries

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultKey is the backend key the query list is stored under.
const DefaultKey = "saved_queries"

const (
	maxNameLen        = 200
	maxContributorLen = 200
	maxQueryLen       = 100000
)

var (
	ErrNameRequired        = errors.New("name is required")
	ErrQueryRequired       = errors.New("query is required")
	ErrContributorRequired = errors.New("contributor is required to save a shared query")
	ErrQueryNotFound       = errors.New("shared query not found")
)

// Query is a single query shared with the team.
type Query struct {
	Name        string    `json:"name"`
	Query       string    `json:"query"`
	Contributor string    `json:"contributor"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Backend persists the entire query list as a single value. Store serializes
// every call, so implementations do not need to be safe for concurrent use.
type Backend interface {
	Load() ([]Query, error)
	Save(queries []Query) error

	// String describes the backend for log messages. It must not include the
	// API token or any other credential.
	String() string
}

type Store struct {
	mu      sync.Mutex
	backend Backend
	queries []Query
	loaded  bool
}

func NewStore(backend Backend) *Store {
	return &Store{backend: backend}
}

// Backend returns the description of the underlying backend.
func (s *Store) Backend() string {
	return s.backend.String()
}

// ensureLoaded pulls the list from the backend the first time it is needed.
// Loading lazily keeps a backend outage from blocking pgweb's startup, and a
// failed load is deliberately not cached so a transient outage recovers on the
// next request rather than needing a restart.
func (s *Store) ensureLoaded() error {
	if s.loaded {
		return nil
	}

	queries, err := s.backend.Load()
	if err != nil {
		return err
	}

	s.queries = sortQueries(queries)
	s.loaded = true

	return nil
}

// List returns every shared query, ordered by name.
func (s *Store) List() ([]Query, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}

	out := make([]Query, len(s.queries))
	copy(out, s.queries)

	return out, nil
}

// Upsert stores a query, replacing any existing query with the same name. The
// in-memory list is only updated once the backend write succeeds, so a failed
// save leaves the store exactly as it was.
func (s *Store) Upsert(query Query) (Query, error) {
	query, err := validate(query)
	if err != nil {
		return query, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(); err != nil {
		return query, err
	}

	next := make([]Query, 0, len(s.queries)+1)
	for _, existing := range s.queries {
		// Names are matched case-insensitively so "Active Users" and "active
		// users" cannot both end up in a shared list.
		if !strings.EqualFold(existing.Name, query.Name) {
			next = append(next, existing)
		}
	}
	next = sortQueries(append(next, query))

	if err := s.backend.Save(next); err != nil {
		return query, err
	}

	s.queries = next

	return query, nil
}

// Update replaces the query stored under originalName. The name itself may
// change, which is why the original is passed separately -- names are the
// identity here, so a rename is a delete plus an insert.
//
// Unlike Upsert, a rename onto a name another query already uses is rejected
// rather than silently merging the two: an edit that quietly destroys someone
// else's query is not a reasonable outcome of pressing "Save Changes".
func (s *Store) Update(originalName string, query Query) (Query, error) {
	query, err := validate(query)
	if err != nil {
		return query, err
	}

	originalName = strings.TrimSpace(originalName)
	if originalName == "" {
		return query, ErrNameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(); err != nil {
		return query, err
	}

	found := false
	next := make([]Query, 0, len(s.queries))

	for _, existing := range s.queries {
		if strings.EqualFold(existing.Name, originalName) {
			found = true
			continue
		}
		if strings.EqualFold(existing.Name, query.Name) {
			return query, fmt.Errorf("a different shared query named %q already exists", existing.Name)
		}
		next = append(next, existing)
	}

	if !found {
		// Someone else renamed or deleted it between the list load and the save
		return query, ErrQueryNotFound
	}

	next = sortQueries(append(next, query))

	if err := s.backend.Save(next); err != nil {
		return query, err
	}

	s.queries = next

	return query, nil
}

// Delete removes the query stored under name.
func (s *Store) Delete(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrNameRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLoaded(); err != nil {
		return err
	}

	found := false
	next := make([]Query, 0, len(s.queries))

	for _, existing := range s.queries {
		if strings.EqualFold(existing.Name, name) {
			found = true
			continue
		}
		next = append(next, existing)
	}

	if !found {
		return ErrQueryNotFound
	}

	if err := s.backend.Save(next); err != nil {
		return err
	}

	s.queries = next

	return nil
}

func validate(query Query) (Query, error) {
	query.Name = strings.TrimSpace(query.Name)
	query.Contributor = strings.TrimSpace(query.Contributor)
	query.Query = strings.TrimSpace(query.Query)

	switch {
	case query.Name == "":
		return query, ErrNameRequired
	case query.Query == "":
		return query, ErrQueryRequired
	// Enforced here and not only in the dialog: the API is reachable directly.
	case query.Contributor == "":
		return query, ErrContributorRequired
	case len(query.Name) > maxNameLen:
		return query, fmt.Errorf("name must be %d characters or fewer", maxNameLen)
	case len(query.Contributor) > maxContributorLen:
		return query, fmt.Errorf("contributor must be %d characters or fewer", maxContributorLen)
	case len(query.Query) > maxQueryLen:
		return query, fmt.Errorf("query must be %d characters or fewer", maxQueryLen)
	}

	if query.UpdatedAt.IsZero() {
		query.UpdatedAt = time.Now().UTC()
	}

	return query, nil
}

func sortQueries(queries []Query) []Query {
	sort.SliceStable(queries, func(i, j int) bool {
		return strings.ToLower(queries[i].Name) < strings.ToLower(queries[j].Name)
	})
	return queries
}
