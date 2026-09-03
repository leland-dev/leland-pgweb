package sharedqueries

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestKV points a CloudflareKV at a local test server instead of the real API.
func newTestKV(t *testing.T, handler http.HandlerFunc) (*CloudflareKV, *httptest.Server) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	backend, err := NewCloudflareKV("acct", "ns", "token", "saved_queries")
	require.NoError(t, err)

	// Rewrite requests to the test server, keeping the real path intact so the
	// URL construction is what is actually under test.
	transport := server.Client().Transport
	backend.client = &http.Client{Transport: rewriteTransport{base: server.URL, next: transport}}

	return backend, server
}

type rewriteTransport struct {
	base string
	next http.RoundTripper
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := req.URL.Parse(t.base)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	return t.next.RoundTrip(req)
}

func Test_CloudflareKVRequiresConfig(t *testing.T) {
	_, err := NewCloudflareKV("", "ns", "token", "")
	assert.EqualError(t, err, "cloudflare kv is missing account id")

	_, err = NewCloudflareKV("", "", "", "")
	assert.EqualError(t, err, "cloudflare kv is missing account id, namespace id, api token")

	backend, err := NewCloudflareKV("acct", "ns", "token", "")
	assert.NoError(t, err)
	assert.Equal(t, DefaultKey, backend.key)
}

func Test_CloudflareKVLoad(t *testing.T) {
	var gotPath, gotAuth string

	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`[{"name":"Users","query":"SELECT 1","contributor":"carter"}]`))
	})

	queries, err := backend.Load()
	assert.NoError(t, err)
	require.Len(t, queries, 1)
	assert.Equal(t, "Users", queries[0].Name)

	assert.Equal(t, "/client/v4/accounts/acct/storage/kv/namespaces/ns/values/saved_queries", gotPath)
	assert.Equal(t, "Bearer token", gotAuth)
}

func Test_CloudflareKVLoadMissingKeyIsEmpty(t *testing.T) {
	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"success":false,"errors":[{"code":10009,"message":"key not found"}]}`))
	})

	queries, err := backend.Load()
	assert.NoError(t, err, "a fresh namespace has no key yet and must not be an error")
	assert.Len(t, queries, 0)
}

func Test_CloudflareKVLoadRejectsUnparseableValue(t *testing.T) {
	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	})

	_, err := backend.Load()
	assert.Error(t, err, "an unparseable value must not read as an empty list")
	assert.Contains(t, err.Error(), "not a valid query list")
}

func Test_CloudflareKVLoadSurfacesAPIError(t *testing.T) {
	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	})

	_, err := backend.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Authentication error (code 10000)")
}

func Test_CloudflareKVSave(t *testing.T) {
	var gotMethod, gotValue, gotContentType string

	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")

		mediaType, params, err := mime.ParseMediaType(gotContentType)
		require.NoError(t, err)
		require.Equal(t, "multipart/form-data", mediaType)

		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if part.FormName() == "value" {
				body, err := io.ReadAll(part)
				require.NoError(t, err)
				gotValue = string(body)
			}
		}

		w.Write([]byte(`{"success":true}`))
	})

	err := backend.Save([]Query{{Name: "Users", Query: "SELECT 1", Contributor: "carter"}})
	assert.NoError(t, err)

	assert.Equal(t, http.MethodPut, gotMethod)
	assert.True(t, strings.HasPrefix(gotContentType, "multipart/form-data"))
	assert.Contains(t, gotValue, `"name":"Users"`)
	assert.Contains(t, gotValue, `"contributor":"carter"`)
}

func Test_CloudflareKVSaveSurfacesAPIError(t *testing.T) {
	backend, _ := newTestKV(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"success":false,"errors":[{"code":10014,"message":"key too long"}]}`))
	})

	err := backend.Save([]Query{{Name: "Users", Query: "SELECT 1", Contributor: "carter"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key too long (code 10014)")
}

func Test_apiErrorMessageFallsBackToBody(t *testing.T) {
	assert.Equal(t, "502 Bad Gateway", apiErrorMessage([]byte("502 Bad Gateway")))
	assert.Equal(t, "empty response body", apiErrorMessage(nil))
	assert.Contains(t, apiErrorMessage([]byte(strings.Repeat("x", 500))), "...")
}
