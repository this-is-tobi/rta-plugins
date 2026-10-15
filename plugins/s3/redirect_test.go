package main

import (
	"context"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// minio-go does not follow a redirect, and the request it signs carries the
// secret key's signature. Pinned against a server that counts what reaches it,
// because that is the library's choice and the guarantee that a CA named for an
// https:// endpoint is never stood behind by a plain hop is this plugin's to
// keep: a library that began to follow would send the signed request on.
func TestARedirectIsNeverFollowedWithASignedRequestInHand(t *testing.T) {
	var sent atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
	}))
	defer plain.Close()

	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+r.URL.Path, code)
		}))
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: secure.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		client, verr := connect(req(t, "s3.bucket.list", map[string]any{
			"endpoint": strings.TrimPrefix(secure.URL, "https://"), "ca-file": ca,
			"access-key": "AKIAEXAMPLE", "secret-key": "s3cret-value"}))
		if verr != nil {
			secure.Close()
			t.Fatalf("connect: %+v", verr)
		}
		if _, err := client.ListBuckets(context.Background()); err == nil {
			t.Errorf("a %d to plain HTTP was answered as a listing of buckets", code)
		}
		secure.Close()
	}
	if n := sent.Load(); n != 0 {
		t.Errorf("%d requests reached the plain-HTTP server a redirect pointed at, signed", n)
	}
}

// A refused port asks the setting question, as every other plugin's does, and
// names the port MinIO serves S3 on. Through a forward the endpoint is the
// host's own, a local port that closed with the call, and asking whether it is
// right is asking about an address nobody typed.
func TestARefusedConnectionAsksAboutTheSettingAndNamesThePort(t *testing.T) {
	refused := &url.Error{Op: "Get", URL: "http://127.0.0.1:9000/", Err: &net.OpError{Op: "dial", Net: "tcp",
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}
	rq := req(t, "s3.overview", map[string]any{"endpoint": "127.0.0.1:9000"})
	got := classify(refused, rq)
	if got.Code != "s3.conn.refused" || !strings.Contains(got.Hint, "is --endpoint right?") ||
		!strings.Contains(got.Hint, "MinIO serves S3 on 9000 by default") {
		t.Errorf("directly: %s %q, want s3.conn.refused asking about --endpoint and naming port 9000", got.Code, got.Hint)
	}
	got = classify(refused, rq.WithProfile("lab", plugin.TunnelKube))
	if got.Code != "s3.conn.refused" || strings.Contains(got.Hint, "--endpoint") ||
		!strings.Contains(got.Hint, "forward") || !strings.Contains(got.Hint, "9000") {
		t.Errorf("through a forward: %s %q, want a hint about the forward that does not ask about --endpoint", got.Code, got.Hint)
	}
}
