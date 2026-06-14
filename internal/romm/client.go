// Package romm is a typed client for the subset of the RomM API the bridge uses:
// listing roms, listing/uploading/updating/downloading saves and states.
package romm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ErrUnauthorized is returned when RomM rejects the token (401/403), e.g. the
// token was rotated or revoked. The caller should evict the cached pairing.
var ErrUnauthorized = errors.New("romm: unauthorized")

// Client talks to a RomM instance as a single user (identified by token).
type Client struct {
	base  string
	baseU *url.URL
	token string
	httpc *http.Client
}

// New returns a Client for baseURL authenticating with the given rmm_ token.
func New(baseURL, token string) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	return &Client{
		base:  baseURL,
		baseU: u,
		token: token,
		httpc: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Asset is a RomM save or state (both are RomAssets with the same shape).
type Asset struct {
	ID             int    `json:"id"`
	RomID          int    `json:"rom_id"`
	FileName       string `json:"file_name"`
	FileNameNoTags string `json:"file_name_no_tags"`
	FileExtension  string `json:"file_extension"`
	FilePath       string `json:"file_path"`
	FullPath       string `json:"full_path"`
	ContentHash    string `json:"content_hash"`
	DownloadPath   string `json:"download_path"`
	Emulator       string `json:"emulator"`
	UpdatedAt      string `json:"updated_at"` // RFC3339; sorts chronologically as a string
}

// NewerThan reports whether a was updated more recently than b. RomM timestamps
// are uniform RFC3339 (+00:00), so lexical comparison is chronological.
func (a Asset) NewerThan(b Asset) bool { return a.UpdatedAt > b.UpdatedAt }

// Rom is the subset of a RomM rom record the bridge indexes.
type Rom struct {
	ID             int    `json:"id"`
	PlatformFsSlug string `json:"platform_fs_slug"`
	FsNameNoExt    string `json:"fs_name_no_ext"`
	FsNameNoTags   string `json:"fs_name_no_tags"`
	Name           string `json:"name"`
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	return c.httpc.Do(req)
}

// readError drains the body and returns a useful error for a non-2xx response.
// 401/403 are wrapped as ErrUnauthorized so callers can evict a stale token.
func readError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	err := fmt.Errorf("romm: %s %s -> %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, bytes.TrimSpace(body))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: %s", ErrUnauthorized, err)
	}
	return err
}

// ExchangePairCode trades a RomM pairing code for a real rmm_ client token.
// The endpoint is unauthenticated (possession of the code is the credential).
// baseURL is the RomM base; code must already be normalized (uppercase, no
// dashes). Returns the raw token and the owning RomM user id.
func ExchangePairCode(ctx context.Context, baseURL, code string) (token string, userID int, err error) {
	body, _ := json.Marshal(map[string]string{"code": code})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/client-tokens/exchange", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, readError(resp)
	}
	var out struct {
		RawToken string `json:"raw_token"`
		UserID   int    `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, fmt.Errorf("decode exchange response: %w", err)
	}
	if out.RawToken == "" {
		return "", 0, fmt.Errorf("romm: exchange returned empty token")
	}
	return out.RawToken, out.UserID, nil
}

// ListSaves returns all saves for the authenticated user.
func (c *Client) ListSaves(ctx context.Context) ([]Asset, error) {
	return c.listAssets(ctx, "/api/saves")
}

// ListStates returns all states for the authenticated user.
func (c *Client) ListStates(ctx context.Context) ([]Asset, error) {
	return c.listAssets(ctx, "/api/states")
}

func (c *Client) listAssets(ctx context.Context, path string) ([]Asset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	var assets []Asset
	if err := json.NewDecoder(resp.Body).Decode(&assets); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return assets, nil
}

// ListRoms returns all roms for the authenticated user, following pagination.
// Requires the token to hold the roms.read scope.
func (c *Client) ListRoms(ctx context.Context) ([]Rom, error) {
	const limit = 500
	var all []Rom
	for offset := 0; ; offset += limit {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(limit))
		q.Set("offset", strconv.Itoa(offset))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/roms?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("romm: GET /api/roms -> %d: %s", resp.StatusCode, bytes.TrimSpace(body))
		}
		// RomM returns a paginated envelope {items,total}; tolerate a bare array too.
		var env struct {
			Items []Rom `json:"items"`
			Total int   `json:"total"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Items == nil {
			var bare []Rom
			if err2 := json.Unmarshal(body, &bare); err2 != nil {
				return nil, fmt.Errorf("decode /api/roms: %w", err)
			}
			return bare, nil
		}
		all = append(all, env.Items...)
		if len(env.Items) < limit || len(all) >= env.Total {
			break
		}
	}
	return all, nil
}

// Download streams an asset's bytes via the generic raw-asset route
// (GET /api/raw/assets/{full_path}), which works for both saves and states.
// fullPath is the asset's full_path field; it is URL-escaped here. The caller
// must close the returned ReadCloser.
func (c *Client) Download(ctx context.Context, fullPath string) (io.ReadCloser, error) {
	u := *c.baseU
	u.Path = "/api/raw/assets/" + fullPath // url.URL escapes Path when sent
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readError(resp)
	}
	return resp.Body, nil
}

// AssetKind distinguishes the two upload endpoints.
type AssetKind int

const (
	KindSave AssetKind = iota
	KindState
)

func (k AssetKind) collection() string {
	if k == KindState {
		return "states"
	}
	return "saves"
}

func (k AssetKind) fileField() string {
	if k == KindState {
		return "stateFile"
	}
	return "saveFile"
}

// Upload creates a new save/state for romID from the given bytes.
func (c *Client) Upload(ctx context.Context, kind AssetKind, romID int, fileName string, content []byte) (*Asset, error) {
	q := url.Values{}
	q.Set("rom_id", strconv.Itoa(romID))
	q.Set("overwrite", "true")
	endpoint := fmt.Sprintf("%s/api/%s?%s", c.base, kind.collection(), q.Encode())
	return c.uploadMultipart(ctx, http.MethodPost, endpoint, kind, fileName, content)
}

// Update replaces the bytes of an existing save/state by id.
func (c *Client) Update(ctx context.Context, kind AssetKind, id int, fileName string, content []byte) (*Asset, error) {
	endpoint := fmt.Sprintf("%s/api/%s/%d", c.base, kind.collection(), id)
	return c.uploadMultipart(ctx, http.MethodPut, endpoint, kind, fileName, content)
}

func (c *Client) uploadMultipart(ctx context.Context, method, endpoint string, kind AssetKind, fileName string, content []byte) (*Asset, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(kind.fileField(), fileName)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(content); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readError(resp)
	}
	var asset Asset
	if err := json.NewDecoder(resp.Body).Decode(&asset); err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}
	return &asset, nil
}
