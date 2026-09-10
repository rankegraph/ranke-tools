// package: main / ranke-git
// type:    io
// job:     the REST client ranke-git talks to a running ranke-db through
// limits:  transport only, over the documented contract — never imports ranke-db itself,
// only ranke-go for wire encoding (-> DESIGN.md, README.md)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
)

// mediaCBORSeq is what POST /contribute reads: an RFC 8742 CBOR sequence.
const mediaCBORSeq = "application/cbor-seq"

// client talks to one ranke-db over HTTP, presenting at most one credential —
// the endpoint rejects two as ambiguous.
type client struct {
	base   string
	token  string
	apiKey string
	http   *http.Client
}

// newClient points a client at a base URL, defaulting a bare host:port to http.
func newClient(base, token, apiKey string) *client {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &client{
		base: strings.TrimRight(base, "/"), token: token, apiKey: apiKey,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// contributionResult mirrors POST /contribute's response — hand-written
// rather than imported, since ranke-git depends on ranke-go, never ranke-db.
type contributionResult struct {
	Head string   `json:"head"`
	Ids  []string `json:"ids"`
}

// apiRefusal is an answer from a server that declined: the status it gave,
// and the contract's error code where it sent one.
type apiRefusal struct {
	status int
	code   string
	msg    string
}

func (e *apiRefusal) Error() string {
	if e.code != "" {
		return fmt.Sprintf("%s (http %d): %s", e.code, e.status, e.msg)
	}
	return fmt.Sprintf("http %d: %s", e.status, e.msg)
}

// contribute merges claims onto branch, returning the new head and the ids
// that landed (empty claims is a no-op). WriteClaim carries only a claim's
// record, referencing content_hash — u is where an external claim's bytes
// are read back from, to send alongside it.
func (c *client) contribute(ctx context.Context, u ranke.Universe, branch string, claims []ranke.Claim) (contributionResult, error) {
	var res contributionResult
	if len(claims) == 0 {
		return res, nil
	}
	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, ranke.WireConstraints{
		Branches: []string{branch}, Referencable: []string{branch}, Creatable: []string{branch},
	})
	sent := map[string]bool{} // content_hash already written, so a blob two claims share goes out once
	for _, claim := range claims {
		if err := w.WriteClaim(branch, claim); err != nil {
			return res, fmt.Errorf("encode claim %s: %w", claim.ID(), err)
		}
		if claim.Node().ContentKind() != ranke.ContentExternal {
			continue
		}
		hash := claim.Node().GetContentHash()
		if sent[hash.String()] {
			continue
		}
		r, err := claim.GetContent(ctx, u)
		if err != nil {
			return res, fmt.Errorf("read content for claim %s: %w", claim.ID(), err)
		}
		payload, err := io.ReadAll(r)
		if err != nil {
			return res, fmt.Errorf("read content for claim %s: %w", claim.ID(), err)
		}
		if err := w.WriteContent(ranke.ContentBlob{Hash: hash, Content: payload}); err != nil {
			return res, fmt.Errorf("encode content for claim %s: %w", claim.ID(), err)
		}
		sent[hash.String()] = true
	}
	out, _, err := c.do(ctx, http.MethodPost, "/contribute", mediaCBORSeq, buf.Bytes())
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("decode contribution result: %w", err)
	}
	return res, nil
}

// advanceClock steers a --dev server's clock to at. A production server has
// no such route, so a 404/501 here is silently swallowed.
func (c *client) advanceClock(ctx context.Context, at time.Time) error {
	body, err := json.Marshal(struct {
		Time time.Time `json:"time"`
	}{Time: at})
	if err != nil {
		return fmt.Errorf("encode dev clock advance: %w", err)
	}
	_, _, err = c.do(ctx, http.MethodPost, "/dev/clock", "application/json", body)
	var refused *apiRefusal
	if errors.As(err, &refused) && (refused.status == http.StatusNotImplemented || refused.status == http.StatusNotFound) {
		return nil
	}
	return err
}

// queryRecord is one query result — ranke-git's own minimal projection, not
// a ranke.Claim: a read-only JSON view, never the signed envelope a Claim
// decodes from.
type queryRecord struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Height      uint64            `json:"height"`
	Fields      map[string]string `json:"fields"`
	ContentHash string            `json:"content_hash"`
}

// query runs an RQL read and decodes its json-seq result. A branch that
// doesn't exist yet answers empty, not an error.
func (c *client) query(ctx context.Context, q ranke.Query) ([]queryRecord, error) {
	body, err := ranke.EncodeQuery(q)
	if err != nil {
		return nil, fmt.Errorf("encode query: %w", err)
	}
	out, _, err := c.do(ctx, http.MethodPost, "/query", "application/json", body)
	if err != nil {
		var refused *apiRefusal
		if errors.As(err, &refused) && refused.status == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return decodeSeq(out)
}

// decodeSeq parses an RFC 7464 JSON text sequence: each record starts with
// RS (0x1e) and runs to the next RS or the end.
func decodeSeq(body []byte) ([]queryRecord, error) {
	var out []queryRecord
	for _, chunk := range bytes.Split(body, []byte{0x1e}) {
		chunk = bytes.TrimSpace(chunk)
		if len(chunk) == 0 {
			continue
		}
		var rec queryRecord
		if err := json.Unmarshal(chunk, &rec); err != nil {
			return nil, fmt.Errorf("decode query record: %w", err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// getClaim fetches one claim's stored envelope by id and decodes it — the
// only way to reach a real ranke.Claim, not just a query's JSON projection.
func (c *client) getClaim(ctx context.Context, branch, id string) (ranke.Claim, error) {
	parsed, err := ranke.ParseId(id)
	if err != nil {
		return nil, fmt.Errorf("parse claim id %q: %w", id, err)
	}
	out, _, err := c.do(ctx, http.MethodGet, "/branches/"+url.PathEscape(branch)+"/claims/"+id, "", nil)
	if err != nil {
		return nil, err
	}
	return ranke.DecodeClaim(parsed, out)
}

// waitReady polls /health rather than sleeping a guessed interval. A
// refusal other than a missing listener stops the wait.
func (c *client) waitReady(ctx context.Context, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		_, _, err := c.do(ctx, http.MethodGet, "/health", "", nil)
		if err == nil {
			return nil
		}
		var refused *apiRefusal
		if errors.As(err, &refused) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no answer from %s/health within %s: %w", c.base, within, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// do sends one request with whatever credential was configured and returns
// the body and content type, mapping a non-2xx onto apiRefusal.
func (c *client) do(ctx context.Context, method, path, contentType string, body []byte) ([]byte, string, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return nil, "", err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.apiKey != "":
		req.Header.Set("X-API-Key", c.apiKey)
	}

	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read %s %s: %w", method, path, err)
	}
	if res.StatusCode/100 != 2 {
		return nil, "", refusal(res.StatusCode, out)
	}
	return out, res.Header.Get("Content-Type"), nil
}

// refusal reads the contract's error payload, falling back to the raw body
// when the answer came from something else — a proxy, say.
func refusal(status int, body []byte) error {
	var e struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Code != "" {
		return &apiRefusal{status: status, code: e.Code, msg: e.Error}
	}
	return &apiRefusal{status: status, msg: strings.TrimSpace(string(body))}
}
