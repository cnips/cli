package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type LoginOptions struct {
	BaseURL  string
	EnvName  string
	Host     string
	Port     int
	Path     string
	Scope    string
	Timeout  time.Duration
	CacheDir string
}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	Env          string    `json:"env"`
	BaseURL      string    `json:"base_url"`
}

func (t *Token) Expired() bool {
	return t == nil || t.AccessToken == "" || time.Until(t.ExpiresAt) < 30*time.Second
}

type ssoResp struct {
	Data struct {
		Issuer    string `json:"issuer"`
		Authority string `json:"authority"`
		ClientID  string `json:"clientId"`
		CID       string `json:"client_id"`
	} `json:"data"`
}

type oidcMeta struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"userinfo_endpoint"`
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

var ErrUnauthorized = errors.New("unauthorized")

func GetToken(ctx context.Context, opts LoginOptions) (*Token, error) {
	defaultLoginOptions(&opts)
	tok, _ := loadTokenCache(opts)
	if !tok.Expired() {
		if err := ValidateToken(ctx, opts.BaseURL, tok.AccessToken); err == nil {
			return tok, nil
		}
		_ = ClearTokenCache(opts)
	}
	return BrowserLogin(ctx, opts)
}

func BrowserLogin(ctx context.Context, opts LoginOptions) (*Token, error) {
	defaultLoginOptions(&opts)

	sso, err := fetchSSO(ctx, opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("fetch sso/clientid: %w", err)
	}
	authority := firstNonEmpty(sso.Data.Issuer, sso.Data.Authority)
	clientID := firstNonEmpty(sso.Data.ClientID, sso.Data.CID)
	if authority == "" || clientID == "" {
		return nil, fmt.Errorf("missing authority/client_id in sso response")
	}
	meta, err := DiscoverOIDC(ctx, authority)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	verifier := randURL(64)
	challenge := s256(verifier)
	state := randURL(24)
	redirect := fmt.Sprintf("http://%s:%d%s", opts.Host, opts.Port, opts.Path)

	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	q.Set("response_mode", "query")
	q.Set("redirect_uri", redirect)
	q.Set("scope", opts.Scope)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authURL := meta.AuthorizationEndpoint + "?" + q.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(opts.Path, func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			errCh <- fmt.Errorf("idp error: %s %s", e, r.URL.Query().Get("error_description"))
			http.Error(w, e, http.StatusBadRequest)
			return
		}
		if r.URL.Query().Get("state") != state {
			errCh <- errors.New("state mismatch")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- errors.New("no code in callback")
			http.Error(w, "no code", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, callbackHTML)
		codeCh <- code
	})

	addr := fmt.Sprintf("%s:%d", opts.Host, opts.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind callback listener on %s: %w", addr, err)
	}
	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	defer srv.Shutdown(context.Background())

	if err := openBrowser(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "could not auto-open browser (%v). Open manually:\n%s\n", err, authURL)
	}

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-time.After(opts.Timeout):
		return nil, errors.New("timeout waiting for login")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tr, err := exchange(ctx, meta.TokenEndpoint, clientID, code, verifier, redirect)
	if err != nil {
		return nil, err
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token endpoint: %s %s", tr.Error, tr.ErrorDesc)
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.ExpiresIn == 0 {
		exp = time.Now().Add(55 * time.Minute)
	}
	tok := &Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		ExpiresAt:    exp,
		Env:          opts.EnvName,
		BaseURL:      opts.BaseURL,
	}
	_ = saveTokenCache(opts, tok)
	return tok, nil
}

func ValidateToken(ctx context.Context, baseURL, token string) error {
	if token == "" {
		return ErrUnauthorized
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mgmtURL(baseURL, "/workspace"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return ErrUnauthorized
	case resp.StatusCode >= 400:
		return fmt.Errorf("validate: HTTP %d", resp.StatusCode)
	default:
		return nil
	}
}

func DiscoverLogin(ctx context.Context, baseURL string) (authority string, clientID string, meta *oidcMeta, err error) {
	sso, err := fetchSSO(ctx, baseURL)
	if err != nil {
		return "", "", nil, err
	}
	authority = firstNonEmpty(sso.Data.Issuer, sso.Data.Authority)
	clientID = firstNonEmpty(sso.Data.ClientID, sso.Data.CID)
	if authority == "" || clientID == "" {
		return "", "", nil, fmt.Errorf("missing authority/client_id")
	}
	meta, err = DiscoverOIDC(ctx, authority)
	return
}

func ClearTokenCache(opts LoginOptions) error {
	defaultLoginOptions(&opts)
	return os.Remove(tokenCachePath(opts))
}

func UserFromToken(token string) *UserInfo {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	user := &UserInfo{
		Subject:           stringClaim(claims, "sub"),
		Email:             stringClaim(claims, "email"),
		Name:              stringClaim(claims, "name"),
		PreferredUsername: stringClaim(claims, "preferred_username"),
		ClientID:          stringClaim(claims, "client_id"),
		Claims:            claims,
	}
	if groups, ok := claims["groups"].([]any); ok {
		for _, raw := range groups {
			groupMap, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			user.Groups = append(user.Groups, UserGroup{
				GroupID:   stringClaim(groupMap, "groupId"),
				GroupType: stringClaim(groupMap, "groupType"),
				Roles:     stringSliceClaim(groupMap, "roles"),
			})
		}
	}
	return user
}

func defaultLoginOptions(o *LoginOptions) {
	o.BaseURL = strings.TrimRight(o.BaseURL, "/")
	if o.EnvName == "" {
		o.EnvName = "default"
	}
	if o.Host == "" {
		o.Host = "localhost"
	}
	if o.Port == 0 {
		o.Port = 3000
	}
	if o.Path == "" {
		o.Path = "/"
	}
	if o.Scope == "" {
		o.Scope = "openid profile"
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.CacheDir == "" {
		if dir, err := os.UserCacheDir(); err == nil {
			o.CacheDir = filepath.Join(dir, "cnips-cli")
		} else {
			home, _ := os.UserHomeDir()
			o.CacheDir = filepath.Join(home, ".cache", "cnips-cli")
		}
	}
}

func fetchSSO(ctx context.Context, baseURL string) (*ssoResp, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mgmtURL(baseURL, "/sso/clientid"), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var s ssoResp
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func DiscoverOIDC(ctx context.Context, authority string) (*oidcMeta, error) {
	u := strings.TrimRight(authority, "/") + "/.well-known/openid-configuration"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discovery %s: HTTP %d: %s", u, resp.StatusCode, body)
	}
	var m oidcMeta
	return &m, json.NewDecoder(resp.Body).Decode(&m)
}

func exchange(ctx context.Context, tokenEndpoint, clientID, code, verifier, redirect string) (*tokenResp, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirect)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var t tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

func mgmtURL(baseURL, path string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/mgmt-srv") {
		return baseURL + ensureLeadingSlash(path)
	}
	return baseURL + "/mgmt-srv" + ensureLeadingSlash(path)
}

func OriginFromAPIURL(apiURL string) string {
	apiURL = strings.TrimRight(apiURL, "/")
	return strings.TrimSuffix(apiURL, "/mgmt-srv")
}

func APIURLFromOrigin(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if strings.HasSuffix(baseURL, "/mgmt-srv") {
		return baseURL
	}
	return baseURL + "/mgmt-srv"
}

// NormalizeAPIURL cleans an explicitly provided mgmt-srv URL and ensures it
// targets the /mgmt-srv base path. It trims surrounding whitespace and any
// trailing slashes, and appends /mgmt-srv when the suffix is missing.
// Loopback/direct hosts (localhost, 127.0.0.1, ::1, 0.0.0.0) are left
// untouched, because a locally run mgmt-srv serves its API at the root.
func NormalizeAPIURL(apiURL string) string {
	apiURL = strings.TrimRight(strings.TrimSpace(apiURL), "/")
	if apiURL == "" {
		return apiURL
	}
	if strings.HasSuffix(apiURL, "/mgmt-srv") {
		return apiURL
	}
	if isLoopbackAPIURL(apiURL) {
		return apiURL
	}
	return apiURL + "/mgmt-srv"
}

func isLoopbackAPIURL(apiURL string) bool {
	parsed, err := url.Parse(apiURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func openBrowser(u string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, u)
	return exec.Command(cmd, args...).Start()
}

func randURL(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func s256(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func tokenCachePath(opts LoginOptions) string {
	sum := sha256.Sum256([]byte(opts.EnvName + "|" + opts.BaseURL))
	name := base64.RawURLEncoding.EncodeToString(sum[:8]) + ".json"
	return filepath.Join(opts.CacheDir, name)
}

func loadTokenCache(opts LoginOptions) (*Token, error) {
	body, err := os.ReadFile(tokenCachePath(opts))
	if err != nil {
		return nil, err
	}
	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveTokenCache(opts LoginOptions, tok *Token) error {
	if err := os.MkdirAll(opts.CacheDir, 0o700); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(tok, "", "  ")
	return os.WriteFile(tokenCachePath(opts), body, 0o600)
}

func ensureLeadingSlash(path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

func stringClaim(claims map[string]any, key string) string {
	if value, ok := claims[key].(string); ok {
		return value
	}
	return ""
}

func stringSliceClaim(claims map[string]any, key string) []string {
	raw, ok := claims[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

const callbackHTML = `<!doctype html><meta charset=utf-8>
<title>Login complete</title>
<body style="font:16px system-ui;padding:2rem">
<h2>Login complete &#10003;</h2><p>You can close this window.</p>
<script>setTimeout(()=>window.close(),800)</script>`
