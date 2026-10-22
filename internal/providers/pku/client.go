package pku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

type tokenSource interface {
	AccessToken() string
	Refresh(context.Context, string) error
}

type anyShareClient struct {
	base      string
	tokens    tokenSource
	client    *http.Client
	objects   *http.Client
	mu        sync.Mutex
	rootCache []pkuEntry
	rootAt    time.Time
}

type pkuEntry struct {
	ID       string    `json:"id"`
	DocID    string    `json:"docid"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Rev      string    `json:"rev"`
	Type     string    `json:"type"`
	Modified time.Time `json:"modified_at"`
	Created  time.Time `json:"created_at"`
}

func (e *pkuEntry) UnmarshalJSON(data []byte) error {
	var v struct {
		ID         string `json:"id"`
		DocID      string `json:"docid"`
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		Rev        string `json:"rev"`
		Type       string `json:"type"`
		Modified   string `json:"modified_at"`
		Created    string `json:"created_at"`
		ModifiedUS int64  `json:"modified"`
		CreatedUS  int64  `json:"create_time"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.ID, e.Name, e.Size, e.Rev, e.Type = v.ID, v.Name, v.Size, v.Rev, v.Type
	if e.ID == "" {
		e.ID = v.DocID
	}
	e.Modified, _ = time.Parse(time.RFC3339Nano, v.Modified)
	e.Created, _ = time.Parse(time.RFC3339Nano, v.Created)
	if e.Modified.IsZero() && v.ModifiedUS > 0 {
		e.Modified = time.UnixMicro(v.ModifiedUS)
	}
	if e.Created.IsZero() && v.CreatedUS > 0 {
		e.Created = time.UnixMicro(v.CreatedUS)
	}
	return nil
}

func newAnyShareClient(endpoint string, tokens tokenSource) (*anyShareClient, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid PKU endpoint")
	}
	secureLocal := u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
	if u.Scheme != "https" && !secureLocal {
		return nil, errors.New("PKU endpoint requires HTTPS except on loopback")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 16
	transport.ResponseHeaderTimeout = 60 * time.Second
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &anyShareClient{
		base:    u.String(),
		tokens:  tokens,
		client:  &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: noRedirect},
		objects: &http.Client{Transport: transport, CheckRedirect: noRedirect},
	}, nil
}

func (c *anyShareClient) request(ctx context.Context, method, path string, body any) ([]byte, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		token := c.tokens.AccessToken()
		req, err := http.NewRequestWithContext(ctx, method, c.base+"/api"+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, safeHTTPError(err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			if err := c.tokens.Refresh(ctx, token); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || resp.StatusCode == http.StatusAccepted {
			return nil, classifyHTTPError(resp.StatusCode, data)
		}
		return data, nil
	}
	return nil, provider.ErrPermission
}

func (c *anyShareClient) post(ctx context.Context, path string, body, out any) error {
	data, err := c.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *anyShareClient) roots(ctx context.Context, fresh bool) ([]pkuEntry, error) {
	c.mu.Lock()
	if !fresh && len(c.rootCache) > 0 && time.Since(c.rootAt) < 5*time.Second {
		result := append([]pkuEntry(nil), c.rootCache...)
		c.mu.Unlock()
		return result, nil
	}
	c.mu.Unlock()
	data, err := c.request(ctx, http.MethodGet, "/efast/v1/entry-doc-lib", nil)
	if err != nil {
		return nil, err
	}
	var entries []pkuEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, entry := range entries {
		counts[entry.Name]++
	}
	for i := range entries {
		entry := &entries[i]
		if entry.ID == "" {
			entry.ID = entry.DocID
		}
		if entry.ID == "" || !provider.ValidName(entry.Name) {
			return nil, errors.New("PKU returned a library with an invalid ID or name")
		}
		entry.Size = -1
		if counts[entry.Name] > 1 {
			entry.Name += " [" + strings.TrimPrefix(entry.ID, "gns://") + "]"
			if !provider.ValidName(entry.Name) {
				return nil, errors.New("PKU returned libraries with indistinguishable names")
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	c.mu.Lock()
	c.rootCache, c.rootAt = append([]pkuEntry(nil), entries...), time.Now()
	c.mu.Unlock()
	return entries, nil
}

func (c *anyShareClient) list(ctx context.Context, id string) ([]pkuEntry, error) {
	var result []pkuEntry
	marker := ""
	seen := map[string]bool{}
	for {
		q := url.Values{"limit": {"1000"}, "sort": {"name"}, "direction": {"asc"}}
		if marker != "" {
			q.Set("marker", marker)
		}
		data, err := c.request(ctx, http.MethodGet, "/efast/v1/folders/"+url.PathEscape(id)+"/sub_objects?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Dirs  []pkuEntry `json:"dirs"`
			Files []pkuEntry `json:"files"`
			Next  string     `json:"next_marker"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, err
		}
		for i := range page.Dirs {
			page.Dirs[i].Size = -1
			page.Dirs[i].ID = firstNonEmpty(page.Dirs[i].ID, page.Dirs[i].DocID)
		}
		for i := range page.Files {
			page.Files[i].ID = firstNonEmpty(page.Files[i].ID, page.Files[i].DocID)
		}
		result = append(result, page.Dirs...)
		result = append(result, page.Files...)
		if page.Next == "" {
			break
		}
		if seen[page.Next] {
			return nil, errors.New("PKU repeated a directory pagination marker")
		}
		seen[page.Next] = true
		marker = page.Next
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (c *anyShareClient) stat(ctx context.Context, entry pkuEntry) (pkuEntry, error) {
	kind := "file"
	if entry.Size == -1 {
		kind = "dir"
	}
	var out pkuEntry
	if err := c.post(ctx, "/efast/v1/"+kind+"/attribute", map[string]any{"docid": entry.ID}, &out); err != nil {
		return pkuEntry{}, err
	}
	if out.ID == "" {
		out.ID = firstNonEmpty(out.ID, out.DocID, entry.ID)
	}
	if out.Name == "" {
		out.Name = entry.Name
	}
	if kind == "dir" {
		out.Size = -1
	}
	return out, nil
}

func (c *anyShareClient) readAt(ctx context.Context, entry pkuEntry, dest []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative read offset")
	}
	if off >= entry.Size || len(dest) == 0 {
		return 0, nil
	}
	length := min(int64(len(dest)), entry.Size-off)
	end := off + length - 1
	var signed struct {
		Request []string `json:"authrequest"`
	}
	if err := c.post(ctx, "/efast/v1/file/osdownload", map[string]any{
		"docid": entry.ID, "rev": entry.Rev, "authtype": "QUERY_STRING",
		"usehttps": strings.HasPrefix(c.base, "https:"),
	}, &signed); err != nil {
		return 0, err
	}
	resp, err := c.signedRequest(ctx, signed.Request, http.Header{"Range": {fmt.Sprintf("bytes=%d-%d", off, end)}})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPartialContent {
		var first, last, total int64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &total); err != nil || first != off || last != end || total != entry.Size {
			return 0, errors.New("PKU object storage returned an inconsistent byte range")
		}
	} else if resp.StatusCode != http.StatusOK || off != 0 || length != entry.Size {
		return 0, errors.New("PKU object storage did not honor the requested byte range")
	}
	return io.ReadFull(resp.Body, dest[:length])
}

func (c *anyShareClient) signedRequest(ctx context.Context, signed []string, headers http.Header) (*http.Response, error) {
	if len(signed) < 2 {
		return nil, errors.New("PKU returned an invalid signed request")
	}
	if !strings.EqualFold(signed[0], http.MethodGet) {
		return nil, errors.New("PKU signed read request is not a GET")
	}
	u, err := url.Parse(signed[1])
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("PKU returned an invalid object URL")
	}
	if strings.HasPrefix(c.base, "https:") && u.Scheme != "https" {
		return nil, errors.New("PKU returned an insecure object URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signed[1], nil)
	if err != nil {
		return nil, errors.New("invalid PKU object request")
	}
	for _, line := range signed[2:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.EqualFold(key, "x-as-userid") {
			continue
		}
		req.Header.Set(key, strings.TrimSpace(value))
	}
	for key, values := range headers {
		req.Header[key] = values
	}
	resp, err := c.objects.Do(req)
	if err != nil {
		return nil, safeHTTPError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("PKU object storage HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func classifyHTTPError(status int, data []byte) error {
	var response struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(data, &response)
	kind := fmt.Errorf("PKU request failed (HTTP %d, code %d)", status, response.Code)
	switch {
	case status == http.StatusAccepted:
		kind = errors.New("PKU operation requires approval")
	case status == http.StatusNotFound || response.Code == 404002005 || response.Code == 404002006 || response.Code == 400007001:
		kind = provider.ErrNotFound
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		kind = provider.ErrPermission
	case status == http.StatusConflict || status == http.StatusPreconditionFailed:
		kind = provider.ErrConflict
	}
	return kind
}

func safeHTTPError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("PKU HTTP transport: %w", ue.Err)
	}
	return err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
