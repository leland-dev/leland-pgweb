package sharedqueries

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cloudflareAPIBase = "https://api.cloudflare.com/client/v4"
	cloudflareTimeout = 10 * time.Second

	// The payload is a small JSON list; the cap only exists so a wrong endpoint
	// or a proxy error page cannot be read into memory unbounded.
	maxResponseSize = 8 << 20
)

// CloudflareKV persists the query list as a single JSON value in a Cloudflare
// Workers KV namespace, over the REST API with net/http. Talking to the API
// directly rather than through an SDK is deliberate: it keeps pgweb's
// dependency list unchanged, which matters for a fork that gets rebased on
// upstream. KV's REST API uses a bearer token, so there is no request signing
// to hand-roll either.
//
// KV is eventually consistent, which the in-memory-authoritative design in
// Store makes a non-issue: the value is read once at startup and every later
// read is served from memory.
type CloudflareKV struct {
	accountID   string
	namespaceID string
	token       string
	key         string
	client      *http.Client
}

func NewCloudflareKV(accountID, namespaceID, token, key string) (*CloudflareKV, error) {
	missing := []string{}
	if accountID == "" {
		missing = append(missing, "account id")
	}
	if namespaceID == "" {
		missing = append(missing, "namespace id")
	}
	if token == "" {
		missing = append(missing, "api token")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("cloudflare kv is missing %s", strings.Join(missing, ", "))
	}

	if key == "" {
		key = DefaultKey
	}

	return &CloudflareKV{
		accountID:   accountID,
		namespaceID: namespaceID,
		token:       token,
		key:         key,
		client:      &http.Client{Timeout: cloudflareTimeout},
	}, nil
}

func (b *CloudflareKV) String() string {
	return fmt.Sprintf("cloudflare kv namespace %s, key %q", b.namespaceID, b.key)
}

func (b *CloudflareKV) valueURL() string {
	return fmt.Sprintf(
		"%s/accounts/%s/storage/kv/namespaces/%s/values/%s",
		cloudflareAPIBase,
		url.PathEscape(b.accountID),
		url.PathEscape(b.namespaceID),
		url.PathEscape(b.key),
	)
}

func (b *CloudflareKV) Load() ([]Query, error) {
	req, err := http.NewRequest(http.MethodGet, b.valueURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare kv read failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("cloudflare kv read failed: %w", err)
	}

	// An absent key is the normal state of a freshly created namespace.
	if resp.StatusCode == http.StatusNotFound {
		return []Query{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudflare kv read returned %s: %s", resp.Status, apiErrorMessage(body))
	}

	// Unlike the rest of the Cloudflare API, the values endpoint returns the
	// raw stored value rather than a {"success":..,"result":..} envelope.
	if len(bytes.TrimSpace(body)) == 0 {
		return []Query{}, nil
	}

	queries := []Query{}
	if err := json.Unmarshal(body, &queries); err != nil {
		// Failing here rather than returning an empty list is the important
		// part: treating an unparseable value as "no queries" would let the
		// next save overwrite the whole team's list with a single entry.
		return nil, fmt.Errorf("cloudflare kv value is not a valid query list: %w", err)
	}

	return queries, nil
}

func (b *CloudflareKV) Save(queries []Query) error {
	payload, err := json.Marshal(queries)
	if err != nil {
		return err
	}

	// The write endpoint takes multipart/form-data with the value in a "value"
	// part, not a raw request body.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("value", string(payload)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPut, b.valueURL(), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := b.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare kv write failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("cloudflare kv write failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflare kv write returned %s: %s", resp.Status, apiErrorMessage(respBody))
	}

	return nil
}

// apiErrorMessage pulls the human-readable part out of a Cloudflare API error
// envelope, falling back to a truncated body when the response is not one.
func apiErrorMessage(body []byte) string {
	var envelope struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}

	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
		}
		return strings.Join(messages, "; ")
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return "empty response body"
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}

	return text
}

// Memory keeps the query list in the process only. It exists so the feature is
// usable in local development without Cloudflare credentials -- anything saved
// through it is lost when pgweb exits.
type Memory struct {
	queries []Query
}

func (b *Memory) Load() ([]Query, error) {
	return append([]Query(nil), b.queries...), nil
}

func (b *Memory) Save(queries []Query) error {
	b.queries = append([]Query(nil), queries...)
	return nil
}

func (b *Memory) String() string {
	return "in-memory, not persisted"
}

var _ Backend = (*CloudflareKV)(nil)
var _ Backend = (*Memory)(nil)
