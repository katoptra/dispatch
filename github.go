package main

// This file does four steps as the GitHub App:
//
//  1. It signs a JWT.
//  2. It finds the installation of the App on the organization.
//  3. It gets an installation token.
//  4. It dispatches a workflow.
//
// It uses no SDK, only three HTTP requests.

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
	"net"
	"net/http"
	"time"
)

// backoff is the interval before each retry of a temporary error: three retries, 70 s in
// total. If a retry cannot start before the deadline of the context, call does not make it.
var backoff = []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}

// StatusError is a GitHub response with a status other than 2xx.
type StatusError struct {
	Status int
	Msg    string
}

func (e *StatusError) Error() string { return e.Msg }

// retryable tells if call can try again after err. With a 4xx other than 408 and 429,
// GitHub rejected the request, and a retry cannot change that. For example: a missing
// workflow file, an App without the permission, or no installation.
//
// call tries a request that starts a run (safe false) again only if GitHub did not start
// the run. GitHub did not start the run if the connection did not open, or if GitHub
// rejected the request before it read it (408, 429). A 5xx or a timeout can follow a
// request that GitHub accepted. Then a retry starts a second run.
func retryable(err error, safe bool) bool {
	var s *StatusError
	if errors.As(err, &s) {
		return s.Status == 408 || s.Status == 429 || s.Status >= 500 && safe
	}
	var op *net.OpError
	return safe || errors.As(err, &op) && op.Op == "dial"
}

// ParseKey reads the private key of the App. GitHub gives a PKCS#1 key. ParseKey also
// accepts PKCS#8. Thus, you can also use a converted key.
func ParseKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private key: no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private key: neither PKCS#1 nor PKCS#8: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key: %T, not RSA", k)
	}
	return rk, nil
}

// AppJWT signs an App JWT with a short life. Its iat is 60 s before now, because GitHub
// rejects an iat after the time on its servers. Its life is 9 minutes, less than the
// 10-minute maximum of GitHub.
func AppJWT(appID string, key *rsa.PrivateKey, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	iat := now.Unix() - 60
	claims, err := json.Marshal(map[string]any{"iat": iat, "exp": iat + 540, "iss": appID})
	if err != nil {
		return "", err
	}
	signed := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// GitHub dispatches workflows as the App. Prepare gets one installation token and keeps it
// only in memory.
type GitHub struct {
	API   string // https://api.github.com
	AppID string
	Org   string // the one installation of this App
	Key   *rsa.PrivateKey
	HTTP  *http.Client
	Sleep func(time.Duration)
	Now   func() time.Time

	token string
}

// Prepare gets the installation token one time in each process.
func (g *GitHub) Prepare(ctx context.Context) error {
	if g.token != "" {
		return nil
	}
	tok, err := g.mint(ctx)
	if err != nil {
		return fmt.Errorf("installation token: %w", err)
	}
	g.token = tok
	return nil
}

func (g *GitHub) Dispatch(ctx context.Context, repo, file string) error {
	if err := g.Prepare(ctx); err != nil {
		return err
	}
	_, err := g.call(ctx, "POST", "/repos/"+repo+"/actions/workflows/"+file+"/dispatches", g.token,
		[]byte(`{"ref":"main","inputs":{}}`), false)
	return err
}

func (g *GitHub) mint(ctx context.Context) (string, error) {
	jwt, err := AppJWT(g.AppID, g.Key, g.Now())
	if err != nil {
		return "", err
	}
	b, err := g.call(ctx, "GET", "/orgs/"+g.Org+"/installation", jwt, nil, true)
	if err != nil {
		return "", err
	}
	var inst struct{ ID int64 }
	if err := json.Unmarshal(b, &inst); err != nil || inst.ID == 0 {
		return "", fmt.Errorf("installation for %s: no id in %.200s", g.Org, b)
	}
	b, err = g.call(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), jwt, nil, true)
	if err != nil {
		return "", err
	}
	var tok struct{ Token string }
	if err := json.Unmarshal(b, &tok); err != nil || tok.Token == "" {
		return "", errors.New("access token: none in the response")
	}
	return tok.Token, nil
}

// call sends one request. After a temporary error, it waits for the next backoff step and
// tries again. safe is true when a second copy of the request has no more effect than the
// first copy. Refer to retryable.
func (g *GitHub) call(ctx context.Context, method, path, auth string, body []byte, safe bool) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		b, err := g.once(ctx, method, path, auth, body)
		if err == nil || !retryable(err, safe) || attempt == len(backoff) || ctx.Err() != nil {
			return b, err
		}
		if dl, ok := ctx.Deadline(); ok && g.Now().Add(backoff[attempt]).After(dl) {
			return b, err
		}
		g.Sleep(backoff[attempt])
	}
}

func (g *GitHub) once(ctx context.Context, method, path, auth string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, g.API+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "katoptra-dispatch")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, &StatusError{res.StatusCode, fmt.Sprintf("%s %s: %d %.500s", method, path, res.StatusCode, b)}
	}
	return b, nil
}
