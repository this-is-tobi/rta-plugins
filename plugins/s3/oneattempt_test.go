package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
