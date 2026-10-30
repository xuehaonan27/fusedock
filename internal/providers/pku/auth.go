// The PKU IAAA login flow is derived from unofficialpku/disk (Apache-2.0),
// https://github.com/unofficialpku/disk. See THIRD_PARTY_NOTICES.md.
package pku

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

const (
	defaultEndpoint  = "https://disk.pku.edu.cn"
	defaultIAAA      = "https://iaaa.pku.edu.cn"
	userAgent        = "FuseDock/0.1 (+https://github.com/xuehaonan27/fusedock)"
	maxAuthBodyBytes = 4 << 20
)

type authSession struct {
	mu      sync.Mutex
	base    *url.URL
	iaaa    *url.URL
	client  *http.Client
	token   string
	onState func(context.Context, State) error
}

// State contains renewable AnyShare cookies. Callers must store it in a
// protected credential store and never log or display it.
type State struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

var ErrStateExpired = errors.New("PKU saved session expired")

type httpStatusError struct {
	Status int
	Method string
	Path   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("authentication %s %s: HTTP %d", e.Method, e.Path, e.Status)
}

func newAuthSession(endpoint, iaaaEndpoint string, onState func(context.Context, State) error) (*authSession, error) {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if iaaaEndpoint == "" {
		iaaaEndpoint = defaultIAAA
	}
	base, err := validatedAuthURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("PKU endpoint: %w", err)
	}
	iaaa, err := validatedAuthURL(iaaaEndpoint)
	if err != nil {
		return nil, fmt.Errorf("PKU IAAA endpoint: %w", err)
	}
	jar, _ := cookiejar.New(nil)
	a := &authSession{base: base, iaaa: iaaa, onState: onState}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	a.client = &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 25 {
				return errors.New("too many authentication redirects")
			}
			if !a.allowed(req.URL) {
				return errors.New("authentication redirected to an unexpected origin")
			}
			return nil
		},
	}
	return a, nil
}

func validatedAuthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid server URL")
	}
	secureLocal := u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
	if u.Scheme != "https" && !secureLocal {
		return nil, errors.New("authentication requires HTTPS except on loopback")
	}
	return u, nil
}

func authOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func (a *authSession) allowed(u *url.URL) bool {
	return u.User == nil && (authOrigin(u) == authOrigin(a.base) || authOrigin(u) == authOrigin(a.iaaa))
}

func (a *authSession) AccessToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

func (a *authSession) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = ""
	a.client.Jar, _ = cookiejar.New(nil)
	a.client.CloseIdleConnections()
}

func (a *authSession) request(ctx context.Context, method, raw, contentType string, body io.Reader) ([]byte, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !a.allowed(u) {
		return nil, nil, errors.New("untrusted authentication endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return nil, nil, errors.New("invalid authentication request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, nil, fmt.Errorf("authentication transport (%s %s): %w", method, u.Host, ue.Err)
		}
		return nil, nil, fmt.Errorf("authentication transport: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAuthBodyBytes))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &httpStatusError{Status: resp.StatusCode, Method: method, Path: req.URL.Path}
	}
	return b, resp.Request.URL, nil
}

func (a *authSession) get(ctx context.Context, raw string) ([]byte, *url.URL, error) {
	return a.request(ctx, http.MethodGet, raw, "", nil)
}

func (a *authSession) form(ctx context.Context, raw string, values url.Values) ([]byte, *url.URL, error) {
	return a.request(ctx, http.MethodPost, raw, "application/x-www-form-urlencoded", strings.NewReader(values.Encode()))
}

type pageProps struct {
	Challenge string `json:"challenge"`
	Auth      struct {
		Third struct {
			Config struct {
				Server string `json:"authServer"`
			} `json:"config"`
		} `json:"thirdauth"`
	} `json:"authconfig"`
}

func parsePage(data []byte) (pageProps, error) {
	z := html.NewTokenizer(bytes.NewReader(data))
	var props pageProps
	found := false
	for {
		t := z.Next()
		if t == html.ErrorToken {
			if !errors.Is(z.Err(), io.EOF) {
				return props, z.Err()
			}
			break
		}
		if t != html.StartTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "script" {
			continue
		}
		id := ""
		for hasAttr {
			key, value, more := z.TagAttr()
			hasAttr = more
			if string(key) == "id" {
				id = string(value)
			}
		}
		if id == "__NEXT_DATA__" && z.Next() == html.TextToken {
			var v struct {
				Props struct {
					Page pageProps `json:"pageProps"`
				} `json:"props"`
			}
			if err := json.Unmarshal(z.Text(), &v); err != nil {
				return props, err
			}
			props, found = v.Props.Page, true
		}
	}
	if !found || props.Challenge == "" {
		return props, errors.New("login page omitted the OAuth challenge")
	}
	return props, nil
}

func (a *authSession) Login(ctx context.Context, username string, password []byte, prompt provider.PromptFunc) error {
	if username == "" || len(password) == 0 {
		return errors.New("PKU username and password are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b, _, err := a.get(ctx, a.base.String()+"/anyshare/oauth2/login?lang=zh-cn&redirect="+url.QueryEscape("/anyshare/"))
	if err != nil {
		return fmt.Errorf("open PKU login: %w", err)
	}
	page, err := parsePage(b)
	if err != nil {
		return err
	}
	if err := a.loginIAAA(ctx, username, password, page, prompt); err != nil {
		return err
	}
	if err := a.updateToken(); err != nil {
		return err
	}
	return a.persist(ctx)
}

func parsePublicKey(raw string) (*rsa.PublicKey, error) {
	if !strings.Contains(raw, "-----BEGIN") {
		raw = "-----BEGIN PUBLIC KEY-----\n" + raw + "\n-----END PUBLIC KEY-----"
	}
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("missing RSA public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		if key, pkcs1Err := x509.ParsePKCS1PublicKey(block.Bytes); pkcs1Err == nil {
			return key, nil
		}
		return nil, errors.New("invalid RSA public key")
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("login key is not RSA")
	}
	return key, nil
}

func encryptPassword(key *rsa.PublicKey, password []byte) (string, error) {
	b, err := rsa.EncryptPKCS1v15(rand.Reader, key, password)
	if err != nil {
		return "", errors.New("password cannot be encrypted with the server key")
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func parseLoginForm(data []byte) (string, url.Values, error) {
	z := html.NewTokenizer(bytes.NewReader(data))
	values := url.Values{}
	action := ""
	for {
		t := z.Next()
		if t == html.ErrorToken {
			break
		}
		if t != html.StartTagToken && t != html.SelfClosingTagToken {
			continue
		}
		name, attrs := z.TagName()
		m := map[string]string{}
		for attrs {
			key, value, more := z.TagAttr()
			attrs = more
			m[string(key)] = string(value)
		}
		if string(name) == "form" {
			action = m["action"]
		}
		if string(name) == "input" && m["name"] != "" {
			values.Set(m["name"], m["value"])
		}
	}
	if action == "" || values.Get("appID") == "" {
		return "", nil, errors.New("SSO page omitted the IAAA form")
	}
	return action, values, nil
}

func ask(ctx context.Context, prompt provider.PromptFunc, p provider.Prompt) (string, error) {
	if prompt == nil {
		return "", fmt.Errorf("%s required; interactive verification is unavailable", p.Kind)
	}
	value, err := prompt(ctx, p)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s cannot be empty", p.Kind)
	}
	return strings.TrimSpace(value), nil
}

func (a *authSession) loginIAAA(ctx context.Context, username string, password []byte, page pageProps, prompt provider.PromptFunc) error {
	a.client.Jar.SetCookies(a.base, []*http.Cookie{
		{Name: "login_challenge", Value: page.Challenge, Path: "/"},
		{Name: "is_previous_login_3rd_party", Value: "true", Path: "/"},
	})
	u, err := a.base.Parse(page.Auth.Third.Config.Server)
	if err != nil || page.Auth.Third.Config.Server == "" || authOrigin(u) != authOrigin(a.base) {
		return errors.New("login page omitted a trusted PKU SSO endpoint")
	}
	b, _, err := a.get(ctx, u.String())
	if err != nil {
		return err
	}
	action, values, err := parseLoginForm(b)
	if err != nil {
		return err
	}
	actionURL, err := url.Parse(action)
	if err != nil || authOrigin(actionURL) != authOrigin(a.iaaa) {
		return errors.New("SSO form does not point to the expected IAAA origin")
	}
	redirect, err := url.Parse(values.Get("redirectUrl"))
	if err != nil || authOrigin(redirect) != authOrigin(a.base) {
		return errors.New("untrusted IAAA callback")
	}
	if _, _, err := a.form(ctx, action, values); err != nil {
		return err
	}
	b, _, err = a.get(ctx, a.iaaa.String()+"/iaaa/getPublicKey.do")
	if err != nil {
		return err
	}
	var keyInfo struct {
		Success bool   `json:"success"`
		Key     string `json:"key"`
	}
	if err := json.Unmarshal(b, &keyInfo); err != nil {
		return err
	}
	if !keyInfo.Success {
		return errors.New("IAAA did not provide a password encryption key")
	}
	key, err := parsePublicKey(keyInfo.Key)
	if err != nil {
		return err
	}
	encrypted, err := encryptPassword(key, password)
	if err != nil {
		return err
	}
	b, _, err = a.get(ctx, a.iaaa.String()+"/iaaa/isShowCode.do")
	if err != nil {
		return err
	}
	var captcha struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(b, &captcha); err != nil {
		return err
	}
	captchaCode := ""
	if captcha.Success {
		image, _, err := a.get(ctx, a.iaaa.String()+"/iaaa/servlet/DrawServlet")
		if err != nil {
			return err
		}
		captchaCode, err = ask(ctx, prompt, provider.Prompt{Kind: provider.PromptCaptcha, Message: "PKU IAAA CAPTCHA", Image: image})
		if err != nil {
			return err
		}
	}
	q := url.Values{"userName": {username}, "appId": {values.Get("appID")}}
	b, _, err = a.get(ctx, a.iaaa.String()+"/iaaa/isMobileAuthen.do?"+q.Encode())
	if err != nil {
		return err
	}
	var mobile struct {
		Success bool   `json:"success"`
		Needed  bool   `json:"isMobileAuthen"`
		Mode    string `json:"authenMode"`
	}
	if err := json.Unmarshal(b, &mobile); err != nil {
		return err
	}
	otp, sms := "", ""
	if mobile.Success && mobile.Needed {
		switch mobile.Mode {
		case "OTP":
			otp, err = ask(ctx, prompt, provider.Prompt{Kind: provider.PromptOTP, Message: "PKU IAAA OTP", Secret: true})
		case "SMS":
			sms, err = ask(ctx, prompt, provider.Prompt{Kind: provider.PromptSMS, Message: "PKU IAAA SMS code", Secret: true})
		default:
			return errors.New("unsupported IAAA second-factor method")
		}
		if err != nil {
			return err
		}
	}
	b, _, err = a.form(ctx, a.iaaa.String()+"/iaaa/oauthlogin.do", url.Values{
		"appid": {values.Get("appID")}, "userName": {username}, "password": {encrypted},
		"randCode": {captchaCode}, "smsCode": {sms}, "otpCode": {otp},
		"remTrustChk": {"false"}, "redirUrl": {redirect.String()},
	})
	if err != nil {
		return err
	}
	var out struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		Errors  struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return err
	}
	if !out.Success || out.Token == "" {
		return fmt.Errorf("IAAA login rejected (code %s); check credentials and required verification", out.Errors.Code)
	}
	query := redirect.Query()
	query.Set("token", out.Token)
	query.Set("_rand", fmt.Sprint(time.Now().UnixNano()))
	redirect.RawQuery = query.Encode()
	_, _, err = a.get(ctx, redirect.String())
	return err
}

func (a *authSession) updateToken() error {
	for _, path := range []string{"/", "/oauth2/", "/anyshare/"} {
		u, _ := a.base.Parse(path)
		for _, cookie := range a.client.Jar.Cookies(u) {
			if cookie.Name == "client.oauth2_token" && cookie.Value != "" {
				token, err := url.PathUnescape(cookie.Value)
				if err != nil {
					return errors.New("invalid access token encoding")
				}
				a.token = token
				return nil
			}
		}
	}
	return errors.New("login did not issue an AnyShare access token")
}

func (a *authSession) stateLocked() State {
	state := State{}
	for _, path := range []string{"/", "/oauth2/", "/anyshare/"} {
		u, _ := a.base.Parse(path)
		for _, cookie := range a.client.Jar.Cookies(u) {
			if cookie.Name == "client.oauth2_token" && state.AccessToken == "" {
				state.AccessToken = cookie.Value
			}
			if cookie.Name == "client.oauth2_refresh_token" {
				state.RefreshToken = cookie.Value
			}
		}
	}
	if state.AccessToken == "" {
		state.AccessToken = a.token
	}
	return state
}

func (a *authSession) persist(ctx context.Context) error {
	if a.onState == nil {
		return nil
	}
	if err := a.onState(ctx, a.stateLocked()); err != nil {
		return fmt.Errorf("persist PKU session: %w", err)
	}
	return nil
}

// Restore applies a saved session without contacting the refresh endpoint.
// Reads refresh lazily after an actual 401, which avoids rewriting the system
// credential store on every CLI invocation or mount.
func (a *authSession) Restore(ctx context.Context, state State) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if state.RefreshToken == "" || state.AccessToken == "" {
		return ErrStateExpired
	}
	cookies := []*http.Cookie{
		{Name: "client.oauth2_token", Value: state.AccessToken, Path: "/", Secure: a.base.Scheme == "https", HttpOnly: true},
		{Name: "client.oauth2_refresh_token", Value: state.RefreshToken, Path: "/", Secure: a.base.Scheme == "https", HttpOnly: true},
	}
	for _, cookie := range cookies {
		if err := cookie.Valid(); err != nil {
			return ErrStateExpired
		}
	}
	a.client.Jar.SetCookies(a.base, cookies)
	a.token = ""
	if err := a.updateToken(); err != nil {
		return ErrStateExpired
	}
	return nil
}

func (a *authSession) Refresh(ctx context.Context, rejected string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshLocked(ctx, rejected)
}

func (a *authSession) refreshLocked(ctx context.Context, rejected string) error {
	if a.token != "" && a.token != rejected {
		return nil
	}
	if _, _, err := a.get(ctx, a.base.String()+"/anyshare/oauth2/login/refreshToken?isforced=true"); err != nil {
		var status *httpStatusError
		if errors.As(err, &status) && (status.Status == http.StatusBadRequest || status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden) {
			return fmt.Errorf("%w: %v", ErrStateExpired, err)
		}
		return fmt.Errorf("refresh PKU session: %w", err)
	}
	if err := a.updateToken(); err != nil {
		return fmt.Errorf("refresh PKU session: %w", err)
	}
	if a.token == rejected {
		return fmt.Errorf("%w: refresh did not issue a new access token", ErrStateExpired)
	}
	return a.persist(ctx)
}
