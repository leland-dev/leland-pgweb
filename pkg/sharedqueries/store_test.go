package sharedqueries

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type failingBackend struct {
	loadErr error
	saveErr error
	saved   [][]Query
	loads   int
}

func (b *failingBackend) Load() ([]Query, error) {
	b.loads++
	if b.loadErr != nil {
		return nil, b.loadErr
	}
	return []Query{}, nil
}

func (b *failingBackend) Save(queries []Query) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	b.saved = append(b.saved, queries)
	return nil
}

func (b *failingBackend) String() string { return "failing" }

func Test_StoreUpsert(t *testing.T) {
	store := NewStore(&Memory{})

	saved, err := store.Upsert(Query{Name: "Zebra", Query: "SELECT 2", Contributor: "carter"})
	assert.NoError(t, err)
	assert.Equal(t, "Zebra", saved.Name)
	assert.False(t, saved.UpdatedAt.IsZero(), "UpdatedAt should be stamped on save")

	_, err = store.Upsert(Query{Name: "apple", Query: "SELECT 1", Contributor: "carter"})
	assert.NoError(t, err)

	list, err := store.List()
	assert.NoError(t, err)
	assert.Len(t, list, 2)

	// Ordered by name, case-insensitively
	assert.Equal(t, "apple", list[0].Name)
	assert.Equal(t, "Zebra", list[1].Name)
}

func Test_StoreUpsertOverwritesByNameIgnoringCase(t *testing.T) {
	store := NewStore(&Memory{})

	_, err := store.Upsert(Query{Name: "Active Users", Query: "SELECT 1", Contributor: "carter"})
	assert.NoError(t, err)

	_, err = store.Upsert(Query{Name: "active users", Query: "SELECT 2", Contributor: "sam"})
	assert.NoError(t, err)

	list, err := store.List()
	assert.NoError(t, err)
	assert.Len(t, list, 1, "a differently-cased name must overwrite, not duplicate")
	assert.Equal(t, "active users", list[0].Name)
	assert.Equal(t, "SELECT 2", list[0].Query)
	assert.Equal(t, "sam", list[0].Contributor)
}

func Test_StoreUpsertTrimsAndValidates(t *testing.T) {
	store := NewStore(&Memory{})

	saved, err := store.Upsert(Query{Name: "  Users  ", Query: "  SELECT 1  ", Contributor: "  carter  "})
	assert.NoError(t, err)
	assert.Equal(t, "Users", saved.Name)
	assert.Equal(t, "SELECT 1", saved.Query)
	assert.Equal(t, "carter", saved.Contributor)

	_, err = store.Upsert(Query{Name: "", Query: "SELECT 1", Contributor: "carter"})
	assert.ErrorIs(t, err, ErrNameRequired)

	_, err = store.Upsert(Query{Name: "Users", Query: "   ", Contributor: "carter"})
	assert.ErrorIs(t, err, ErrQueryRequired)

	// The dialog disables "Save Shared" without a contributor, but the API is
	// reachable directly, so the store has to enforce it too.
	_, err = store.Upsert(Query{Name: "Users", Query: "SELECT 1", Contributor: ""})
	assert.ErrorIs(t, err, ErrContributorRequired)
}

func Test_StoreUpsertLeavesListUnchangedWhenSaveFails(t *testing.T) {
	backend := &failingBackend{saveErr: errors.New("kv unavailable")}
	store := NewStore(backend)

	_, err := store.Upsert(Query{Name: "Users", Query: "SELECT 1", Contributor: "carter"})
	assert.EqualError(t, err, "kv unavailable")

	list, err := store.List()
	assert.NoError(t, err)
	assert.Len(t, list, 0, "a failed backend write must not be reflected in memory")
}

func Test_StoreRetriesLoadAfterFailure(t *testing.T) {
	backend := &failingBackend{loadErr: errors.New("kv unavailable")}
	store := NewStore(backend)

	_, err := store.List()
	assert.EqualError(t, err, "kv unavailable")
	assert.Equal(t, 1, backend.loads)

	// A failed load must not be cached, otherwise a transient outage would
	// require restarting the task to recover.
	backend.loadErr = nil

	list, err := store.List()
	assert.NoError(t, err)
	assert.Len(t, list, 0)
	assert.Equal(t, 2, backend.loads)

	// Once loaded, the backend is not hit again on reads.
	_, err = store.List()
	assert.NoError(t, err)
	assert.Equal(t, 2, backend.loads)
}

func Test_StoreListReturnsACopy(t *testing.T) {
	store := NewStore(&Memory{})

	_, err := store.Upsert(Query{Name: "Users", Query: "SELECT 1", Contributor: "carter"})
	assert.NoError(t, err)

	list, err := store.List()
	assert.NoError(t, err)
	list[0].Name = "mutated"

	fresh, err := store.List()
	assert.NoError(t, err)
	assert.Equal(t, "Users", fresh[0].Name, "callers must not be able to mutate stored queries")
}
