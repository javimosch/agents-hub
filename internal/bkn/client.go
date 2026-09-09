// Package bkn is the HTTP client for a bkn backend core.
//
// agents-hub stores nothing itself. Agents, threads and messages are
// documents in bkn collections, identity is bkn auth, and tenancy is enforced
// by the collection's declared access policy rather than by filters this
// package remembers to add. That is the point of the split: a bug here cannot
// hand one user another user's threads, because this client never gets to
// state whose data it is asking for.
package bkn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Record is a stored document: a flat object carrying its own "id".
type Record map[string]any

// Str reads a string field, returning "" when absent or of another type.
func (r Record) Str(k string) string {
	s, _ := r[k].(string)
	return s
}

// Num reads a numeric field as float64 (JSON's only number type).
func (r Record) Num(k string) float64 {
	f, _ := r[k].(float64)
	return f
}

// Tokens is what a login or refresh returns. The field names are bkn's, not
// a convention: access_token is a stateless JWT with a short TTL, and
// refresh_token is the stateful, revocable half.
type Tokens struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	TokenType string `json:"token_type"`
	ExpiresIn int    `json:"expires_in"`
	User      Record `json:"user"`
	Org       string `json:"org,omitempty"`
}

// APIError is a non-2xx answer from bkn, carrying bkn's own error type so a
// caller can branch on the category rather than on the message text.
type APIError struct {
	Status  int
	Type    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("bkn returned %d", e.Status)
	}
	return e.Message
}

// NotFound reports whether the error is a bkn 404.
func NotFound(err error) bool {
	var e *APIError
	if ok := asAPI(err, &e); ok {
		return e.Status == http.StatusNotFound
	}
	return false
}

// Unauthorized reports whether the error is a bkn 401/403.
func Unauthorized(err error) bool {
	var e *APIError
	if ok := asAPI(err, &e); ok {
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	}
	return false
}

func asAPI(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}

// TransportError is a request that never got an answer: DNS, refused
// connection, timeout. It is separate from APIError because the two demand
// opposite responses -- a transport failure is worth retrying, a 400 is not.
type TransportError struct{ Err error }

func (e *TransportError) Error() string { return e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// Transport reports whether err is a transport failure.
func Transport(err error) bool {
	_, ok := err.(*TransportError)
	return ok
}

// FromBackend reports whether err came from bkn at all, as opposed to from
// the caller's own validation.
func FromBackend(err error) bool {
	if _, ok := err.(*APIError); ok {
		return true
	}
	return Transport(err)
}

// Client talks to one bkn instance with one token.
//
// RefreshToken and OnTokens together make expiry invisible to callers. A bkn
// access token is a stateless JWT that lives 15 minutes; without this, every
// command run more than 15 minutes after a login fails with 90 and the agent
// has to know to log in again -- which is a password prompt in the middle of
// an automated task.
type Client struct {
	BaseURL      string
	Token        string
	RefreshToken string
	// OnTokens is called with a rotated pair so the caller can persist it.
	// bkn rotates the refresh token on use: losing the new one costs the
	// session, so this is not optional bookkeeping.
	OnTokens func(Tokens)
	HTTP     *http.Client
}

// New builds a client. A zero timeout means 30s: an agent waiting on a hung
// backend forever is worse than an agent told the backend is unreachable.
func New(baseURL, token string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// WithToken returns a copy bound to a different token. Used for the one call
// that legitimately needs the admin token, so the admin token never leaks onto
// the serving path by sharing a client.
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.Token = token
	return &cp
}

func (c *Client) do(method, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = b
	}
	err := c.attempt(method, path, raw, out)
	// One retry, and only for an expired token. A second 401 after a
	// successful refresh means the credential is wrong rather than stale, and
	// retrying that is just a slower way to fail.
	if err != nil && c.expired(err) && c.RefreshToken != "" {
		if rerr := c.rotate(); rerr == nil {
			return c.attempt(method, path, raw, out)
		}
	}
	return err
}

// expired reports a 401, which is what bkn answers for a token past its TTL.
// A 403 is a policy refusal and refreshing cannot help.
func (c *Client) expired(err error) bool {
	e, ok := err.(*APIError)
	return ok && e.Status == http.StatusUnauthorized
}

// rotate trades the refresh token for a new pair and adopts it.
func (c *Client) rotate() error {
	fresh := &Client{BaseURL: c.BaseURL, HTTP: c.HTTP}
	tokens, err := fresh.Refresh(c.RefreshToken)
	if err != nil {
		return err
	}
	c.Token, c.RefreshToken = tokens.Access, tokens.Refresh
	if c.OnTokens != nil {
		c.OnTokens(tokens)
	}
	return nil
}

func (c *Client) attempt(method, path string, raw []byte, out any) error {
	var rdr io.Reader
	if raw != nil {
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return &TransportError{Err: fmt.Errorf("bkn unreachable at %s: %w", c.BaseURL, err)}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		ae := &APIError{Status: resp.StatusCode}
		var env struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &env) == nil {
			ae.Type, ae.Message = env.Error.Type, env.Error.Message
		}
		if ae.Message == "" {
			ae.Message = strings.TrimSpace(string(respBody))
		}
		return ae
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// Health probes the instance. It is the cheapest way to tell "bkn is down"
// from "your token is wrong", two failures an agent must not conflate.
func (c *Client) Health() error {
	return c.do(http.MethodGet, "/_health", nil, nil)
}

// Login exchanges credentials for tokens. org may be empty.
func (c *Client) Login(email, password, org string) (Tokens, error) {
	body := map[string]any{"email": email, "password": password}
	if org != "" {
		body["org"] = org
	}
	var out struct {
		Tokens Tokens `json:"tokens"`
	}
	err := c.do(http.MethodPost, "/v1/auth/login", body, &out)
	return out.Tokens, err
}

// Refresh trades a refresh token for a new pair.
func (c *Client) Refresh(refresh string) (Tokens, error) {
	var out struct {
		Tokens Tokens `json:"tokens"`
	}
	err := c.do(http.MethodPost, "/v1/auth/refresh", map[string]any{"refresh_token": refresh}, &out)
	return out.Tokens, err
}

// Me returns the caller bkn believes it is talking to.
func (c *Client) Me() (Record, error) {
	var out Record
	err := c.do(http.MethodGet, "/v1/auth/me", nil, &out)
	return out, err
}

// Declare creates or updates a collection and its access policy. Admin only.
func (c *Client) Declare(ref string, body map[string]any) error {
	return c.do(http.MethodPut, "/v1/store/"+ref, body, nil)
}

// Create writes a new document into a collection.
func (c *Client) Create(ref string, doc map[string]any) (Record, error) {
	var out struct {
		Record Record `json:"record"`
	}
	err := c.do(http.MethodPost, "/v1/store/"+ref, doc, &out)
	return out.Record, err
}

// Get reads one document by id.
func (c *Client) Get(ref, id string) (Record, error) {
	var out struct {
		Record Record `json:"record"`
	}
	err := c.do(http.MethodGet, "/v1/store/"+ref+"/"+url.PathEscape(id), nil, &out)
	return out.Record, err
}

// List reads a page of a collection. Filters go in q as field=value, with
// bkn's operator prefixes available (?created_at=gt:...).
func (c *Client) List(ref string, q url.Values) ([]Record, error) {
	path := "/v1/store/" + ref
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Records []Record `json:"records"`
	}
	err := c.do(http.MethodGet, path, nil, &out)
	return out.Records, err
}

// Patch merges fields into an existing document.
func (c *Client) Patch(ref, id string, doc map[string]any) (Record, error) {
	var out struct {
		Record Record `json:"record"`
	}
	err := c.do(http.MethodPatch, "/v1/store/"+ref+"/"+url.PathEscape(id), doc, &out)
	return out.Record, err
}

// ScriptResult is what a script run returned.
type ScriptResult struct {
	OK    bool           `json:"ok"`
	Value any            `json:"value"`
	Run   map[string]any `json:"run"`
}

// RunScript executes a bkn script with this client's token.
//
// Nothing here supplies an admin credential: whether the call is allowed is
// decided by the script's own declared run policy, next to the code being
// run. A 403 means the operator has not opened that script, and no amount of
// retrying from here will change it.
func (c *Client) RunScript(name string, input any) (*ScriptResult, error) {
	if input == nil {
		input = map[string]any{}
	}
	var out ScriptResult
	err := c.do(http.MethodPost, "/v1/script/"+url.PathEscape(name)+"/run", input, &out)
	// A script that ran and failed answers 422 with a body worth reading: the
	// value, the logs and the run id. That is a result, not a transport
	// error, so it is unwrapped rather than raised.
	var ae *APIError
	if e, ok := err.(*APIError); ok && e.Status == http.StatusUnprocessableEntity {
		ae = e
	}
	if ae != nil {
		return &ScriptResult{OK: false, Run: map[string]any{"error": ae.Message}}, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a document.
func (c *Client) Delete(ref, id string) error {
	return c.do(http.MethodDelete, "/v1/store/"+ref+"/"+url.PathEscape(id), nil, nil)
}
