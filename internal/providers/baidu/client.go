package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuehaonan/fusedock/pkg/provider"
)

const (
	maxResponseBytes = 32 << 20
	userAgent        = "pan.baidu.com"
)

type client struct {
	apiBase   *url.URL
	oauthBase *url.URL
	appKey    string
	secretKey string
	username  string
	api       *http.Client
	oauth     *http.Client
	download  *http.Client

	onState      func(context.Context, []byte) error
	mu           sync.Mutex
	accessToken  string
	refreshToken string
	expiresAt    time.Time
}

func newClient(apiEndpoint, oauthEndpoint, appKey, secretKey string, onState func(context.Context, []byte) error) (*client, error) {
	apiURL, err := validatedEndpoint(apiEndpoint, "Baidu API endpoint")
	if err != nil {
		return nil, err
	}
	oauthURL, err := validatedEndpoint(oauthEndpoint, "Baidu OAuth endpoint")
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	downloadTransport := transport.Clone()
	downloadClient := &http.Client{Transport: downloadTransport, Timeout: 30 * time.Minute}
	downloadClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 10 {
			return errors.New("too many download redirects")
		}
		if !trustedDownloadURL(req.URL, apiURL) {
			return errors.New("download redirected to an untrusted host")
		}
		return nil
	}
	return &client{
		apiBase: apiURL, oauthBase: oauthURL, appKey: appKey, secretKey: secretKey,
		api:      &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: noRedirect},
		oauth:    &http.Client{Transport: transport.Clone(), Timeout: 60 * time.Second, CheckRedirect: noRedirect},
		download: downloadClient, onState: onState,
	}, nil
}

func validatedEndpoint(raw, label string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, fmt.Errorf("%s is invalid", label)
	}
	loopback := u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")
	if u.Scheme != "https" && !loopback {
		return nil, fmt.Errorf("%s requires HTTPS", label)
	}
	return u, nil
}

func trustedDownloadURL(u *url.URL, apiURL *url.URL) bool {
	if u.User != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1") {
		return true
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".baidu.com", ".baidupcs.com", ".bcebos.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return host == strings.ToLower(apiURL.Hostname())
}

type apiError struct {
	Code    int
	Status  int
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("Baidu API error (errno=%d, HTTP=%d)", e.Code, e.Status)
}

func (c *client) restore(state savedState) {
	c.mu.Lock()
	c.username = state.Username
	c.accessToken = state.AccessToken
	c.refreshToken = state.RefreshToken
	if state.ExpiresAt > 0 {
		c.expiresAt = time.Unix(state.ExpiresAt, 0)
	}
	c.mu.Unlock()
}

func (c *client) expired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.expiresAt.IsZero() && time.Now().Add(30*time.Second).After(c.expiresAt)
}

func (c *client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accessToken
}

func (c *client) setUsername(ctx context.Context, username string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.username == username {
		return nil
	}
	c.username = username
	return c.persistLocked(ctx)
}

func (c *client) do(ctx context.Context, method string, base *url.URL, path string, query url.Values, out any) error {
	return c.doJSON(ctx, c.api, method, base, path, query, out, false)
}

func (c *client) doOAuth(ctx context.Context, path string, query url.Values, out any) error {
	return c.doJSON(ctx, c.oauth, http.MethodGet, c.oauthBase, path, query, out, true)
}

func (c *client) doJSON(ctx context.Context, httpClient *http.Client, method string, base *url.URL, path string, query url.Values, out any, allowOAuthError bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	u := *base
	u.Path = path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return errors.New("invalid Baidu request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return safeTransportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return errors.New("read Baidu response failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if allowOAuthError && out != nil {
			if err := json.Unmarshal(data, out); err == nil {
				return nil
			}
		}
		return &apiError{Status: resp.StatusCode}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Baidu returned invalid JSON")
		}
	}
	return nil
}

func safeTransportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("Baidu transport failed: %w", ue.Err)
	}
	return errors.New("Baidu transport failed")
}

func (c *client) listEndpoint(ctx context.Context, path string, start, limit int) (listResponse, error) {
	query := url.Values{
		"method": {"list"}, "dir": {path}, "start": {strconv.Itoa(start)},
		"limit": {strconv.Itoa(limit)}, "order": {"name"}, "desc": {"0"}, "web": {"web"},
	}
	var result listResponse
	err := c.apiRequest(ctx, "/rest/2.0/xpan/file", query, &result)
	return result, err
}

func (c *client) list(ctx context.Context, path string) ([]meta, error) {
	if !strings.HasPrefix(path, "/") {
		return nil, errors.New("invalid Baidu directory path")
	}
	const pageSize = 1000
	result := make([]meta, 0)
	for start := 0; ; start += pageSize {
		page, err := c.listEndpoint(ctx, path, start, pageSize)
		if err != nil {
			return nil, err
		}
		for _, file := range page.List {
			if file.FsID <= 0 || file.Path == "" {
				return nil, errors.New("Baidu returned incomplete file metadata")
			}
			result = append(result, file.meta(""))
		}
		if len(page.List) < pageSize {
			break
		}
	}
	return result, nil
}

func (c *client) apiRequest(ctx context.Context, path string, query url.Values, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		query.Set("access_token", c.currentToken())
		err := c.do(ctx, http.MethodGet, c.apiBase, path, query, out)
		if err == nil {
			if apiErr := decodeAPIError(out); apiErr != nil {
				if attempt == 0 && tokenError(apiErr.Code) {
					if refreshErr := c.refresh(ctx); refreshErr != nil {
						return refreshErr
					}
					continue
				}
				return apiErr
			}
			return nil
		}
		var ae *apiError
		if errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden) && attempt == 0 {
			if refreshErr := c.refresh(ctx); refreshErr != nil {
				return refreshErr
			}
			continue
		}
		return err
	}
	return provider.ErrPermission
}

func decodeAPIError(out any) *apiError {
	if value, ok := out.(listResponse); ok && value.Errno != 0 {
		return &apiError{Code: value.Errno}
	}
	if value, ok := out.(*listResponse); ok && value.Errno != 0 {
		return &apiError{Code: value.Errno}
	}
	if value, ok := out.(*downloadResponse); ok && value.Errno != 0 {
		return &apiError{Code: value.Errno}
	}
	return nil
}

func tokenError(errno int) bool {
	switch errno {
	case -6, 111, 20016, 20017:
		return true
	default:
		return false
	}
}

func (c *client) refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshToken == "" {
		return fmt.Errorf("%w: Baidu refresh token is missing", provider.ErrPermission)
	}
	query := url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {c.refreshToken},
		"client_id": {c.appKey}, "client_secret": {c.secretKey},
	}
	var result tokenResponse
	if err := c.doOAuth(ctx, "/oauth/2.0/token", query, &result); err != nil {
		return fmt.Errorf("refresh Baidu token: %w", err)
	}
	if result.Error != "" || result.AccessToken == "" {
		return fmt.Errorf("%w: Baidu refresh rejected: %s", provider.ErrPermission, result.Error)
	}
	c.accessToken = result.AccessToken
	if result.RefreshToken != "" {
		c.refreshToken = result.RefreshToken
	}
	if result.ExpiresIn > 0 {
		c.expiresAt = time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)
	}
	return c.persistLocked(ctx)
}

func (c *client) persistLocked(ctx context.Context) error {
	if c.onState == nil {
		return nil
	}
	state := savedState{
		Version: stateVersion, APIEndpoint: c.apiBase.String(), OAuthEndpoint: c.oauthBase.String(),
		AppKey: c.appKey, Username: c.username, AccessToken: c.accessToken, RefreshToken: c.refreshToken,
		ExpiresAt: c.expiresAt.Unix(),
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	defer clear(data)
	if err := c.onState(ctx, data); err != nil {
		return fmt.Errorf("persist Baidu session: %w", err)
	}
	return nil
}

func (c *client) userIdentity(ctx context.Context) (string, error) {
	query := url.Values{"access_token": {c.currentToken()}}
	var result userInfoResponse
	if err := c.doOAuth(ctx, "/rest/2.0/passport/users/getInfo", query, &result); err != nil {
		return "", err
	}
	if result.Error != "" || result.ErrorMsg != "" || result.OpenID == "" {
		return "", errors.New("Baidu user identity request failed")
	}
	return result.OpenID, nil
}

func (c *client) downloadURL(ctx context.Context, id string) (string, error) {
	query := url.Values{"method": {"filemetas"}, "fsids": {"[" + id + "]"}, "dlink": {"1"}}
	var result downloadResponse
	if err := c.apiRequest(ctx, "/rest/2.0/xpan/multimedia", query, &result); err != nil {
		return "", err
	}
	if len(result.List) != 1 || result.List[0].Dlink == "" {
		return "", errors.New("Baidu did not return a download link")
	}
	u, err := url.Parse(result.List[0].Dlink)
	if err != nil || !trustedDownloadURL(u, c.apiBase) {
		return "", errors.New("Baidu returned an untrusted download URL")
	}
	q := u.Query()
	q.Set("access_token", c.currentToken())
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *client) close() {
	c.api.CloseIdleConnections()
	c.oauth.CloseIdleConnections()
	c.download.CloseIdleConnections()
}
