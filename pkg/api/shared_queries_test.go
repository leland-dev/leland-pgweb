package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sosedoff/pgweb/pkg/sharedqueries"
)

// sharedQueryRouter wires just the shared query routes, without the connection
// check the real /api group applies, so the handlers can be exercised without a
// database.
func sharedQueryRouter(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)

	previous := SharedQueryStore
	SharedQueryStore = sharedqueries.NewStore(&sharedqueries.Memory{})
	t.Cleanup(func() { SharedQueryStore = previous })

	router := gin.New()
	router.GET("/shared_queries", requireSharedQueries(), GetSharedQueries)
	router.POST("/shared_queries", requireSharedQueries(), CreateSharedQuery)
	router.PUT("/shared_queries", requireSharedQueries(), UpdateSharedQuery)
	router.DELETE("/shared_queries", requireSharedQueries(), DeleteSharedQuery)

	return router
}

func do(t *testing.T, router *gin.Engine, method, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()

	var req *http.Request
	if form == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	return rr
}

func listNames(t *testing.T, router *gin.Engine) []string {
	t.Helper()

	rr := do(t, router, http.MethodGet, "/shared_queries", nil)
	require.Equal(t, 200, rr.Code, rr.Body.String())

	queries := []sharedqueries.Query{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &queries))

	names := []string{}
	for _, q := range queries {
		names = append(names, q.Name)
	}

	return names
}

func errorMessage(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()

	body := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))

	message, _ := body["error"].(string)
	return message
}

func Test_SharedQueriesCRUD(t *testing.T) {
	router := sharedQueryRouter(t)

	assert.Equal(t, []string{}, listNames(t, router))

	rr := do(t, router, http.MethodPost, "/shared_queries", url.Values{
		"name":        {"Users"},
		"query":       {"SELECT 1"},
		"contributor": {"carter"},
	})
	require.Equal(t, 200, rr.Code, rr.Body.String())
	assert.Equal(t, []string{"Users"}, listNames(t, router))

	// Rename plus an edit of every other field
	rr = do(t, router, http.MethodPut, "/shared_queries", url.Values{
		"original_name": {"Users"},
		"name":          {"Active Users"},
		"query":         {"SELECT 2"},
		"contributor":   {"sam"},
	})
	require.Equal(t, 200, rr.Code, rr.Body.String())

	updated := sharedqueries.Query{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	assert.Equal(t, "Active Users", updated.Name)
	assert.Equal(t, "SELECT 2", updated.Query)
	assert.Equal(t, "sam", updated.Contributor)
	assert.Equal(t, []string{"Active Users"}, listNames(t, router))

	// The name lives in the query string because Go does not parse a DELETE body
	rr = do(t, router, http.MethodDelete, "/shared_queries?name=Active+Users", nil)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	assert.Equal(t, []string{}, listNames(t, router))
}

func Test_SharedQueriesDeleteNeedsAName(t *testing.T) {
	router := sharedQueryRouter(t)

	rr := do(t, router, http.MethodDelete, "/shared_queries", nil)
	assert.Equal(t, 400, rr.Code)
	assert.Equal(t, "name is required", errorMessage(t, rr))

	rr = do(t, router, http.MethodDelete, "/shared_queries?name=Nope", nil)
	assert.Equal(t, 400, rr.Code)
	assert.Equal(t, "shared query not found", errorMessage(t, rr))
}

func Test_SharedQueriesNameWithSpecialCharacters(t *testing.T) {
	router := sharedQueryRouter(t)

	// Names are free text: slashes would not survive a URL path segment, which
	// is why the API keeps them in the payload and the query string.
	name := "reports/weekly \"top\" & Bob's"

	rr := do(t, router, http.MethodPost, "/shared_queries", url.Values{
		"name":        {name},
		"query":       {"SELECT 1"},
		"contributor": {"carter"},
	})
	require.Equal(t, 200, rr.Code, rr.Body.String())
	assert.Equal(t, []string{name}, listNames(t, router))

	rr = do(t, router, http.MethodDelete, "/shared_queries?name="+url.QueryEscape(name), nil)
	require.Equal(t, 200, rr.Code, rr.Body.String())
	assert.Equal(t, []string{}, listNames(t, router))
}

func Test_SharedQueriesUpdateRejectsRenameOntoAnother(t *testing.T) {
	router := sharedQueryRouter(t)

	for _, name := range []string{"Users", "Orders"} {
		rr := do(t, router, http.MethodPost, "/shared_queries", url.Values{
			"name": {name}, "query": {"SELECT 1"}, "contributor": {"carter"},
		})
		require.Equal(t, 200, rr.Code, rr.Body.String())
	}

	rr := do(t, router, http.MethodPut, "/shared_queries", url.Values{
		"original_name": {"Users"},
		"name":          {"orders"},
		"query":         {"SELECT 1"},
		"contributor":   {"carter"},
	})
	assert.Equal(t, 400, rr.Code)
	assert.Equal(t, `a different shared query named "Orders" already exists`, errorMessage(t, rr))
	assert.Equal(t, []string{"Orders", "Users"}, listNames(t, router))
}

func Test_SharedQueriesDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previous := SharedQueryStore
	SharedQueryStore = nil
	t.Cleanup(func() { SharedQueryStore = previous })

	router := gin.New()
	router.GET("/shared_queries", requireSharedQueries(), GetSharedQueries)

	rr := do(t, router, http.MethodGet, "/shared_queries", nil)
	assert.Equal(t, 400, rr.Code)
	assert.Equal(t, "shared queries are disabled", errorMessage(t, rr))
}
