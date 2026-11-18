package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(cfg *Config, srv *httptest.Server) *Client {
	cfg.APIBase = srv.URL
	c := NewClient(cfg)
	c.backoff = time.Millisecond
	return c
}

func TestRegistrationAndRemoveTokenWithPAT(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer pat123" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"token":"tok-` + r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:] + `"}`))
	}))
	defer srv.Close()

	cfg := &Config{Scope: "org", Org: "octo", AccessToken: "pat123", TokenPath: "/orgs/octo/actions/runners"}
	c := newTestClient(cfg, srv)
	reg, err := c.RegistrationToken(context.Background())
	if err != nil || reg != "tok-registration-token" {
		t.Fatalf("registration: %q %v", reg, err)
	}
	rm, err := c.RemoveToken(context.Background())
	if err != nil || rm != "tok-remove-token" {
		t.Fatalf("remove: %q %v", rm, err)
	}
	want := "POST /orgs/octo/actions/runners/registration-token,POST /orgs/octo/actions/runners/remove-token"
	if strings.Join(paths, ",") != want {
		t.Errorf("paths = %v", paths)
	}
}

func genKeyPEM(t *testing.T, pkcs8 bool) (*rsa.PrivateKey, []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if pkcs8 {
		der, _ := x509.MarshalPKCS8PrivateKey(k)
		return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// verifyJWT checks the RS256 signature and returns the claims.
func verifyJWT(t *testing.T, tok string, pub *rsa.PublicKey) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("bad signature: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestAppAuthFlow(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		key, pemBytes := genKeyPEM(t, pkcs8)
		var seen []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			switch r.URL.Path {
			case "/repos/octo/hello/installation":
				claims := verifyJWT(t, auth, &key.PublicKey)
				if claims["iss"] != float64(4242) {
					t.Errorf("iss = %v", claims["iss"])
				}
				exp, iat := claims["exp"].(float64), claims["iat"].(float64)
				if exp-iat != 600 || time.Unix(int64(iat), 0).After(time.Now()) {
					t.Errorf("bad iat/exp: %v %v", iat, exp)
				}
				_, _ = w.Write([]byte(`{"id":99}`))
			case "/app/installations/99/access_tokens":
				verifyJWT(t, auth, &key.PublicKey)
				_, _ = w.Write([]byte(`{"token":"inst-token"}`))
			case "/repos/octo/hello/actions/runners/registration-token":
				if auth != "inst-token" {
					t.Errorf("runner token requested with %q", auth)
				}
				_, _ = w.Write([]byte(`{"token":"reg"}`))
			default:
				http.NotFound(w, r)
			}
		}))
		cfg := &Config{
			Scope: "repo", Owner: "octo", Repo: "hello", AppID: "4242", AppKey: pemBytes,
			TokenPath: "/repos/octo/hello/actions/runners",
		}
		tok, err := newTestClient(cfg, srv).RegistrationToken(context.Background())
		srv.Close()
		if err != nil || tok != "reg" {
			t.Fatalf("pkcs8=%v: %q %v (%v)", pkcs8, tok, err, seen)
		}
		if len(seen) != 3 {
			t.Errorf("pkcs8=%v: calls = %v", pkcs8, seen)
		}
	}
}

func TestAppJWTClientIDIsString(t *testing.T) {
	key, pemBytes := genKeyPEM(t, false)
	jwt, err := appJWT("Iv1.abc123", pemBytes, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if verifyJWT(t, jwt, &key.PublicKey)["iss"] != "Iv1.abc123" {
		t.Error("client ID should stay a string")
	}
}

func TestBadKey(t *testing.T) {
	if _, err := appJWT("1", []byte("not pem"), time.Now()); err == nil {
		t.Error("expected error for non-PEM key")
	}
}

func TestRetriesOn5xxButNotOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, `{"message":"boom"}`, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"token":"ok"}`))
	}))
	cfg := &Config{AccessToken: "t", TokenPath: "/x"}
	tok, err := newTestClient(cfg, srv).RegistrationToken(context.Background())
	srv.Close()
	if err != nil || tok != "ok" || calls.Load() != 3 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, calls.Load())
	}

	calls.Store(0)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err = newTestClient(cfg, srv).RegistrationToken(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	if strings.Contains(err.Error(), "Bearer") || strings.Contains(err.Error(), cfg.AccessToken+"\"") {
		t.Errorf("error leaks credentials: %v", err)
	}
}

func TestRunnerTokenOnlyCannotCallAPI(t *testing.T) {
	c := NewClient(&Config{RunnerToken: "r"})
	if _, err := c.RemoveToken(context.Background()); err == nil {
		t.Error("expected error when only RUNNER_TOKEN is configured")
	}
}
