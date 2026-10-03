package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/view"
)

// The suite drives transfers against servers that are meant to fail, and the
// library's ten attempts with a backoff between would be waited out in every
// one: a failed download and the conformance run's dry runs against a closed
// port took several seconds each. The test that is about the retries asks for
// them back.
func init() { transferAttempts = 1 }

// A call is one attempt. minio-go asks ten times, with a backoff between, before
// it gives up on anything it reads as transient — a refused connection among
// it — so an endpoint that was down answered three seconds late with the
// refusal it would have had at once: measured, against a closed port, 3.4
// seconds for s3.overview where the redis, etcd and pg plugins answered in 40
// milliseconds. A server answering 503 stands for the failure here: counting
// what reaches it needs no wait on a timer.
func TestACallThatFailsIsNotAskedAgainBehindTheCallersBack(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>ServiceUnavailable</Code><Message>try later</Message></Error>`))
	}))
	t.Cleanup(srv.Close)

	_, err := runBucketList(t.Context(), reqFor(t, "s3.bucket.list", endpointOf(t, srv), map[string]any{}))
	if err == nil {
		t.Fatal("an endpoint answering 503 to everything produced a view")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the endpoint was asked %d times, want 1", got)
	}
}

// A transfer is the exception. A bulk download or upload is hundreds of
// requests under one refusal-on-failure — a run that fails takes the whole
// directory with it — so one transient 503 in the middle of a backup must not
// cost the backup: the transfer client keeps the library's own retries, and a
// server that fails twice and then answers is a call that succeeds.
func TestATransferKeepsTheLibrarysRetries(t *testing.T) {
	transferAttempts = 0
	t.Cleanup(func() { transferAttempts = 1 })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if hits.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>ServiceUnavailable</Code><Message>try later</Message></Error>`))
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><ListAllMyBucketsResult><Owner><ID>x</ID></Owner><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(srv.Close)

	r := reqFor(t, "s3.bucket.list", endpointOf(t, srv), map[string]any{})
	_, err := withTransferClient(t.Context(), r, func(ctx context.Context, client *minio.Client) (view.View, error) {
		_, err := client.ListBuckets(ctx)
		return nil, err
	})
	if err != nil {
		t.Errorf("a transfer that met two 503s and then an answer failed: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("the endpoint was asked %d times, want 3", got)
	}
}
