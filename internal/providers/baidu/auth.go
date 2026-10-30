package baidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

// Authorize starts Baidu's OAuth device-code flow. The browser never receives
// the app secret, and no local callback listener is required.
func (Factory) Authorize(ctx context.Context, req provider.AuthorizeRequest) (provider.Authorization, error) {
	appKey := strings.TrimSpace(string(req.Secrets["app_key"]))
	secretKey := string(req.Secrets["secret_key"])
	if appKey == "" || secretKey == "" {
		return provider.Authorization{}, errors.New("Baidu app_key and secret_key are required for authorization")
	}
	oauthEndpoint := optionOrDefault(req.Options, "oauth_endpoint", defaultOAuth)
	apiEndpoint := optionOrDefault(req.Options, "api_endpoint", defaultAPI)
	scope := strings.TrimSpace(req.Options["scope"])
	if scope == "" {
		scope = defaultScope
	}
	client, err := newClient(apiEndpoint, oauthEndpoint, appKey, secretKey, nil)
	if err != nil {
		return provider.Authorization{}, err
	}
	defer client.close()

	query := url.Values{
		"response_type": {"device_code"}, "openapi": {"xpansdk"},
		"client_id": {appKey}, "scope": {scope},
	}
	var device deviceCodeResponse
	if err := client.doOAuth(ctx, "/oauth/2.0/device/code", query, &device); err != nil {
		return provider.Authorization{}, fmt.Errorf("start Baidu device authorization: %w", err)
	}
	if device.Error != "" || device.DeviceCode == "" || device.UserCode == "" || device.VerificationURL == "" {
		return provider.Authorization{}, fmt.Errorf("Baidu device authorization failed: %s", device.Error)
	}
	verification, err := url.Parse(device.VerificationURL)
	if err != nil || verification.Scheme != "https" || verification.Host == "" || verification.User != nil {
		return provider.Authorization{}, errors.New("Baidu returned an untrusted verification URL")
	}
	if req.Notify != nil {
		req.Notify(fmt.Sprintf("Open %s and enter code %s", verification.String(), device.UserCode))
	}
	if req.OpenURL != nil {
		if err := req.OpenURL(verification.String()); err != nil {
			if req.Notify != nil {
				req.Notify("Could not open the browser automatically; open the URL above manually.")
			}
		}
	}

	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	expires := time.Duration(device.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	deadline := time.Now().Add(expires)
	for {
		if time.Now().After(deadline) {
			return provider.Authorization{}, errors.New("Baidu device authorization expired")
		}
		select {
		case <-ctx.Done():
			return provider.Authorization{}, ctx.Err()
		case <-time.After(interval):
		}
		query := url.Values{
			"grant_type": {"device_token"}, "openapi": {"xpansdk"},
			"code": {device.DeviceCode}, "client_id": {appKey}, "client_secret": {secretKey},
		}
		var token tokenResponse
		if err := client.doOAuth(ctx, "/oauth/2.0/token", query, &token); err != nil {
			return provider.Authorization{}, fmt.Errorf("poll Baidu device authorization: %w", err)
		}
		if token.Error == "authorization_pending" {
			continue
		}
		if token.Error == "slow_down" {
			interval += 5 * time.Second
			continue
		}
		if token.Error != "" || token.AccessToken == "" {
			return provider.Authorization{}, fmt.Errorf("Baidu device authorization rejected: %s", token.Error)
		}
		client.mu.Lock()
		client.accessToken = token.AccessToken
		client.refreshToken = token.RefreshToken
		client.expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
		client.mu.Unlock()
		username, err := client.userIdentity(ctx)
		if err != nil {
			return provider.Authorization{}, fmt.Errorf("read Baidu account identity: %w", err)
		}
		client.username = username
		state := savedState{
			Version: stateVersion, APIEndpoint: apiEndpoint, OAuthEndpoint: oauthEndpoint, AppKey: appKey,
			Username: username, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
			ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		}
		stateData, err := json.Marshal(state)
		if err != nil {
			return provider.Authorization{}, err
		}
		return provider.Authorization{
			Username: username,
			Secrets: map[string][]byte{
				"app_key": []byte(appKey), "secret_key": []byte(secretKey),
			},
			State: stateData,
		}, nil
	}
}
