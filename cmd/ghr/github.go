package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Client talks to the GitHub REST API. It only ever needs three calls:
// (App auth) find the installation and mint an installation token, and
// request a registration / remove token for the runner.
type Client struct {
	cfg     *Config
	http    *http.Client
	now     func() time.Time
	backoff time.Duration
}

func NewClient(cfg *Config) *Client {
	return &Client{
		cfg:     cfg,
		http:    &http.Client{Timeout: 30 * time.Second},
		now:     time.Now,
		backoff: time.Second,
	}
}

// RegistrationToken returns a short-lived token for `config.sh`.
func (c *Client) RegistrationToken(ctx context.Context) (string, error) {
	return c.runnerToken(ctx, "registration-token")
}

// RemoveToken returns a short-lived token for `config.sh remove`.
func (c *Client) RemoveToken(ctx context.Context) (string, error) {
	return c.runnerToken(ctx, "remove-token")
}

func (c *Client) runnerToken(ctx context.Context, kind string) (string, error) {
	bearer, err := c.bearer(ctx)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := c.do(ctx, http.MethodPost, c.cfg.APIBase+c.cfg.TokenPath+"/"+kind, bearer, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("%s: empty token in response", kind)
	}
	return out.Token, nil
}

// bearer returns the credential for API calls: the personal access token,
// or a freshly minted GitHub App installation token.
func (c *Client) bearer(ctx context.Context) (string, error) {
	if c.cfg.AccessToken != "" {
		return c.cfg.AccessToken, nil
	}
	if c.cfg.AppID == "" {
		return "", errors.New("no API credentials configured (only RUNNER_TOKEN was given)")
	}
	jwt, err := appJWT(c.cfg.AppID, c.cfg.AppKey, c.now())
	if err != nil {
		return "", err
	}
	// The installation endpoint for the repo/org works for both user and
	// organisation accounts and needs no pagination.
	var instPath string
	switch c.cfg.Scope {
	case "org":
		instPath = "/orgs/" + c.cfg.Org + "/installation"
	default:
		instPath = "/repos/" + c.cfg.Owner + "/" + c.cfg.Repo + "/installation"
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, c.cfg.APIBase+instPath, jwt, &inst); err != nil {
		return "", fmt.Errorf("find app installation: %w", err)
	}
	var tok struct {
		Token string `json:"token"`
	}
	u := c.cfg.APIBase + "/app/installations/" + strconv.FormatInt(inst.ID, 10) + "/access_tokens"
	if err := c.do(ctx, http.MethodPost, u, jwt, &tok); err != nil {
		return "", fmt.Errorf("create installation token: %w", err)
	}
	if tok.Token == "" {
		return "", errors.New("create installation token: empty token in response")
	}
	return tok.Token, nil
}

// do performs one API call with a small retry on network errors, 429 and 5xx.
func (c *Client) do(ctx context.Context, method, url, bearer string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(c.backoff << (attempt - 1)):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "gh-runner-ghr/"+version)

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if err := json.Unmarshal(body, out); err != nil {
				return fmt.Errorf("%s %s: decode response: %w", method, url, err)
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = apiError(method, url, resp.StatusCode, body)
			continue
		default:
			return apiError(method, url, resp.StatusCode, body)
		}
	}
	return lastErr
}

func apiError(method, url string, status int, body []byte) error {
	var m struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &m)
	if m.Message == "" {
		m.Message = http.StatusText(status)
	}
	return fmt.Errorf("%s %s: HTTP %d: %s", method, url, status, m.Message)
}

// appJWT builds the RS256 JWT GitHub Apps use to authenticate as the app.
// It is implemented with the standard library only, to keep the binary free
// of third-party code.
func appJWT(appID string, pemKey []byte, now time.Time) (string, error) {
	key, err := parseRSAKey(pemKey)
	if err != nil {
		return "", err
	}
	iat := now.Add(-60 * time.Second).Unix() // tolerate clock drift
	var iss any = appID                      // client IDs are strings
	if n, err := strconv.ParseInt(appID, 10, 64); err == nil {
		iss = n // numeric app IDs are numbers
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	pl, _ := json.Marshal(map[string]any{"iat": iat, "exp": iat + 600, "iss": iss})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(pl)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("sign app JWT: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func parseRSAKey(pemKey []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(bytes.TrimSpace(pemKey))
	if block == nil {
		return nil, errors.New("APP_PRIVATE_KEY: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("APP_PRIVATE_KEY: not a valid PKCS#1 or PKCS#8 RSA key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("APP_PRIVATE_KEY: key is not RSA")
	}
	return rk, nil
}
