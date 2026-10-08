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
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testKey = func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}()

// github is a test server for the three endpoints. The /dispatches endpoint sends the
// statuses in dispatch, in sequence, and then 204.
func github(t *testing.T, dispatch ...int) (*GitHub, *[]string, *[]time.Duration) {
	t.Helper()
	var hits []string
	var slept []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hits = append(hits, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/orgs/katoptra/installation":
			w.Write([]byte(`{"id":42}`))
		case r.URL.Path == "/app/installations/42/access_tokens" && r.Method == "POST":
			w.WriteHeader(201)
			w.Write([]byte(`{"token":"ghs_test"}`))
		case strings.HasSuffix(r.URL.Path, "/dispatches"):
			if r.Header.Get("Authorization") != "Bearer ghs_test" {
				t.Errorf("dispatch authorization %q", r.Header.Get("Authorization"))
			}
			if string(body) != `{"ref":"main","inputs":{}}` {
				t.Errorf("dispatch body %s", body)
			}
			status := 204
			if len(dispatch) > 0 {
				status, dispatch = dispatch[0], dispatch[1:]
			}
			w.WriteHeader(status)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	g := &GitHub{
		API: srv.URL, AppID: "123", Org: "katoptra", Key: testKey, HTTP: srv.Client(),
		Sleep: func(d time.Duration) { slept = append(slept, d) }, Now: time.Now,
	}
	return g, &hits, &slept
}

func TestDispatchMintsOnceAndDispatches(t *testing.T) {
	g, hits, _ := github(t)
	ctx := context.Background()
	must(t, g.Dispatch(ctx, "katoptra/ctan", "sync.yml"))
	must(t, g.Dispatch(ctx, "katoptra/tlnet", "sync.yml"))
	want := []string{
		"GET /orgs/katoptra/installation",
		"POST /app/installations/42/access_tokens",
		"POST /repos/katoptra/ctan/actions/workflows/sync.yml/dispatches",
		"POST /repos/katoptra/tlnet/actions/workflows/sync.yml/dispatches",
	}
	if strings.Join(*hits, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests\n%s\nwant\n%s", strings.Join(*hits, "\n"), strings.Join(want, "\n"))
	}
}

func TestTransientStatusIsRetried(t *testing.T) {
	g, hits, slept := github(t, 408, 429)
	must(t, g.Dispatch(context.Background(), "katoptra/ctan", "sync.yml"))
	if len(*hits) != 5 || len(*slept) != 2 || (*slept)[0] != 10*time.Second || (*slept)[1] != 20*time.Second {
		t.Errorf("hits %v, slept %v; want three dispatch attempts after 10s and 20s", *hits, *slept)
	}
}

func TestServerErrorOnDispatchIsNotRetried(t *testing.T) {
	g, hits, slept := github(t, 502)
	err := g.Dispatch(context.Background(), "katoptra/ctan", "sync.yml")
	if err == nil || len(*slept) != 0 || len(*hits) != 3 {
		t.Errorf("err %v, hits %v; a 502 may follow an accepted dispatch, so no retry", err, *hits)
	}
}

func TestUnopenedConnectionIsRetried(t *testing.T) {
	g, _, slept := github(t)
	g.API, g.token = "http://127.0.0.1:1", "ghs_test"
	if err := g.Dispatch(context.Background(), "katoptra/ctan", "sync.yml"); err == nil || len(*slept) != 3 {
		t.Errorf("err %v, slept %v; a refused connection never reached GitHub, so three retries", err, *slept)
	}
}

func TestRetryable(t *testing.T) {
	status := func(n int) error { return &StatusError{Status: n} }
	for _, c := range []struct {
		name       string
		err        error
		safe, want bool
	}{
		{"5xx on a mint", status(503), true, true},
		{"5xx on a dispatch", status(503), false, false},
		{"429 on a dispatch", status(429), false, true},
		{"404", status(404), true, false},
		{"dial failure on a dispatch", &net.OpError{Op: "dial", Err: errors.New("refused")}, false, true},
		{"read timeout on a dispatch", &net.OpError{Op: "read", Err: errors.New("timeout")}, false, false},
		{"read timeout on a mint", &net.OpError{Op: "read", Err: errors.New("timeout")}, true, true},
	} {
		if got := retryable(c.err, c.safe); got != c.want {
			t.Errorf("%s: retryable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRetryStopsAtTheDeadline(t *testing.T) {
	g, _, slept := github(t, 429, 429, 429, 429)
	start := time.Now()
	g.Now = func() time.Time { return start }
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(15*time.Second))
	defer cancel()
	err := g.Dispatch(ctx, "katoptra/ctan", "sync.yml")
	if err == nil || len(*slept) != 1 {
		t.Errorf("err %v, slept %v; want the 10s retry and not the 20s one past the deadline", err, *slept)
	}
}

func TestRetriesRunOut(t *testing.T) {
	g, _, slept := github(t, 429, 429, 429, 429)
	err := g.Dispatch(context.Background(), "katoptra/ctan", "sync.yml")
	if err == nil || len(*slept) != 3 {
		t.Errorf("err %v, slept %v; want failure after three retries", err, *slept)
	}
}

func TestClientErrorIsFatal(t *testing.T) {
	g, _, slept := github(t, 404)
	err := g.Dispatch(context.Background(), "katoptra/ctan", "sync.yml")
	if err == nil || !strings.Contains(err.Error(), "404") || len(*slept) != 0 {
		t.Errorf("err %v, slept %v; want an immediate 404", err, *slept)
	}
}

func TestPrepareFailsWithoutAnInstallation(t *testing.T) {
	g, hits, _ := github(t)
	g.Org = "nobody"
	if err := g.Prepare(context.Background()); err == nil || len(*hits) != 1 {
		t.Errorf("err %v, hits %v; want one refused installation lookup", err, *hits)
	}
}

func TestAppJWTVerifies(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	jwt, err := AppJWT("123", testKey, now)
	must(t, err)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	must(t, err)
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	must(t, rsa.VerifyPKCS1v15(&testKey.PublicKey, crypto.SHA256, sum[:], sig))
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	must(t, err)
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	must(t, json.Unmarshal(raw, &claims))
	if claims.Iss != "123" || claims.Iat != now.Unix()-60 || claims.Exp != now.Unix()+480 {
		t.Errorf("claims %+v", claims)
	}
}

func TestParseKeyTakesBothEncodings(t *testing.T) {
	pkcs8, err := x509.MarshalPKCS8PrivateKey(testKey)
	must(t, err)
	for name, block := range map[string]*pem.Block{
		"pkcs1": {Type: "RSA " + "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testKey)},
		"pkcs8": {Type: "PRIVATE " + "KEY", Bytes: pkcs8},
	} {
		k, err := ParseKey(pem.EncodeToMemory(block))
		if err != nil || !k.Equal(testKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseKey([]byte("not a key")); err == nil {
		t.Error("garbage parsed as a key")
	}
}
