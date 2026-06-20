// Package sdk is the SP8 reference Go client for the OpenLeaderboard HTTP API.
// It is the canonical example other-language SDKs (Unity/C#, JS) mirror.
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrNotFound is returned when a member or board does not exist.
var ErrNotFound = errors.New("sdk: not found")

// Entry mirrors a ranking entry returned by the API.
type Entry struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
	Rank   int64   `json:"rank"`
	Exact  bool    `json:"exact"`
}

// Window is one temporal dimension when defining a board.
type Window struct {
	Kind     string `json:"kind"`
	CustomID string `json:"custom_id,omitempty"`
}

// BoardDef defines a leaderboard.
type BoardDef struct {
	Board        string   `json:"board"`
	SortOrder    string   `json:"sort_order,omitempty"`
	UpdatePolicy string   `json:"update_policy,omitempty"`
	TieBreak     string   `json:"tie_break,omitempty"`
	ScoreBits    uint     `json:"score_bits,omitempty"`
	Windows      []Window `json:"windows,omitempty"`
}

// QueryOpts selects the physical board (segment/window) to read.
type QueryOpts struct {
	Segment string
	Window  string
}

func (o QueryOpts) values() url.Values {
	v := url.Values{}
	if o.Segment != "" {
		v.Set("segment", o.Segment)
	}
	if o.Window != "" {
		v.Set("window", o.Window)
	}
	return v
}

// Client talks to an OpenLeaderboard server.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates a client. baseURL is e.g. "http://localhost:8080".
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, ErrNotFound
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("sdk: %s %s -> %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// CreateBoard defines a leaderboard.
func (c *Client) CreateBoard(ctx context.Context, def BoardDef) error {
	_, err := c.do(ctx, http.MethodPost, "/v1/boards", def, nil)
	return err
}

// Submission is a score to submit.
type Submission struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
	// Time is the event time of the score; it selects the time-window bucket
	// (e.g. the daily board). Set it to the session start so a run crossing
	// midnight counts for the day it began. Zero value means server receive time.
	Time     time.Time `json:"time,omitempty"`
	Segments []string  `json:"segments,omitempty"`
	Idem     string    `json:"idem,omitempty"`
	Sig      string    `json:"sig,omitempty"`
	TS       int64     `json:"ts,omitempty"`
	Nonce    string    `json:"nonce,omitempty"`
}

// Submit posts a score. accepted is false for a deduplicated submission.
func (c *Client) Submit(ctx context.Context, board string, sub Submission) (accepted bool, err error) {
	var out struct {
		Accepted bool `json:"accepted"`
	}
	_, err = c.do(ctx, http.MethodPost, "/v1/boards/"+url.PathEscape(board)+"/scores", sub, &out)
	if err != nil {
		return false, err
	}
	return out.Accepted, nil
}

// GetRank returns a member's exact rank. Returns ErrNotFound if absent.
func (c *Client) GetRank(ctx context.Context, board, member string, opts QueryOpts) (Entry, error) {
	v := opts.values()
	v.Set("member", member)
	var e Entry
	_, err := c.do(ctx, http.MethodGet, "/v1/boards/"+url.PathEscape(board)+"/rank?"+v.Encode(), nil, &e)
	return e, err
}

// GetApproxRank returns a member's approximate rank from the board's score
// histogram (the returned Entry has Exact=false). The board must be created with
// ApproxRank enabled; on very large boards this avoids an exact rank scan.
// Returns ErrNotFound if the member is absent.
func (c *Client) GetApproxRank(ctx context.Context, board, member string, opts QueryOpts) (Entry, error) {
	v := opts.values()
	v.Set("member", member)
	v.Set("approx", "true")
	var e Entry
	_, err := c.do(ctx, http.MethodGet, "/v1/boards/"+url.PathEscape(board)+"/rank?"+v.Encode(), nil, &e)
	return e, err
}

func (c *Client) listEndpoint(ctx context.Context, path string) ([]Entry, error) {
	var out struct {
		Entries []Entry `json:"entries"`
	}
	_, err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Entries, err
}

// Top returns the top n entries.
func (c *Client) Top(ctx context.Context, board string, n int, opts QueryOpts) ([]Entry, error) {
	v := opts.values()
	v.Set("n", strconv.Itoa(n))
	return c.listEndpoint(ctx, "/v1/boards/"+url.PathEscape(board)+"/top?"+v.Encode())
}

// Neighbors returns the member plus up to k entries on each side.
func (c *Client) Neighbors(ctx context.Context, board, member string, k int, opts QueryOpts) ([]Entry, error) {
	v := opts.values()
	v.Set("member", member)
	v.Set("k", strconv.Itoa(k))
	return c.listEndpoint(ctx, "/v1/boards/"+url.PathEscape(board)+"/neighbors?"+v.Encode())
}

// Friends ranks an explicit set of members against each other.
func (c *Client) Friends(ctx context.Context, board string, memberIDs []string, opts QueryOpts) ([]Entry, error) {
	var out struct {
		Entries []Entry `json:"entries"`
	}
	path := "/v1/boards/" + url.PathEscape(board) + "/friends?" + opts.values().Encode()
	_, err := c.do(ctx, http.MethodPost, path, map[string]any{"members": memberIDs}, &out)
	return out.Entries, err
}
