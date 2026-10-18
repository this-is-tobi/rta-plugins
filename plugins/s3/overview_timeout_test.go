package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/view"
)

// An endpoint that takes the connection and never answers held the overview for
// a minute, which is the library's own wait for the headers of a response, and
// a tile for as long again at every refresh.

func silentEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv
}

func boundOverviewAt(t *testing.T, d time.Duration) {
	t.Helper()
	old := overviewTimeout
	overviewTimeout = d
	t.Cleanup(func() { overviewTimeout = old })
}

func TestAnEndpointThatNeverAnswersIsGivenUpOnAtTheOverviewsBound(t *testing.T) {
	boundOverviewAt(t, 150*time.Millisecond)
	srv := silentEndpoint(t)
	for name, values := range map[string]map[string]any{
		"the endpoint":        {},
		"a bucket":            {"bucket": "mirai-ocr-preprod"},
		"a bucket and detail": {"bucket": "mirai-ocr-preprod", "detail": true},
		"the endpoint detail": {"detail": true},
	} {
		started := time.Now()
		_, err := runOverview(context.Background(), reqFor(t, "s3.overview", endpointOf(t, srv), values))
		verr, ok := err.(*view.Error)
		if !ok || verr.Code != "s3.timeout" {
			t.Fatalf("%s: err = %#v, want s3.timeout", name, err)
		}
		if took := time.Since(started); took > 2*time.Second {
			t.Errorf("%s: gave up after %s, want about the bound of 150ms", name, took)
		}
		if !strings.Contains(verr.Message, "did not answer in time") || !strings.Contains(verr.Hint, "firewall that drops") {
			t.Errorf("%s: %q — %q", name, verr.Message, verr.Hint)
		}
	}
}

// The bound is the overview's. A listing of buckets is not held to it, and a
// download or an upload, which moves the data, is not held to any.
func TestTheOverviewsBoundIsNotTheBoundOfAnyOtherCall(t *testing.T) {
	boundOverviewAt(t, 20*time.Millisecond)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult><Buckets></Buckets></ListAllMyBucketsResult>`))
	}))
	t.Cleanup(slow.Close)
	if _, err := runBucketList(context.Background(), reqFor(t, "s3.bucket.list", endpointOf(t, slow), map[string]any{})); err != nil {
		t.Errorf("s3.bucket.list was cut short by the overview's bound: %v", err)
	}
}

// A 5xx is the service failing, not a setting wrong; the hint was the page of
// every input. A refusal that a retry will not change keeps its own.
func TestAServerErrorIsToldToBeRetried(t *testing.T) {
	r := reqFor(t, "s3.overview", "127.0.0.1:1", map[string]any{})
	for _, tc := range []struct {
		status int
		code   string
	}{
		{500, minio.InternalError}, {502, "BadGateway"}, {503, "SlowDown"}, {504, "GatewayTimeout"},
	} {
		verr := classify(minio.ErrorResponse{StatusCode: tc.status, Code: tc.code, Message: "try later"}, r)
		if verr.Code != "s3.request.failed" || !strings.Contains(verr.Hint, "the service is overloaded or restarting; retry") {
			t.Errorf("%d %s: %s — %s", tc.status, tc.code, verr.Code, verr.Hint)
		}
	}
	for _, status := range []int{400, 403, 409} {
		verr := classify(minio.ErrorResponse{StatusCode: status, Code: "SomethingElse", Message: "no"}, r)
		if strings.Contains(verr.Hint, "retry") {
			t.Errorf("%d: %q advises retrying a refusal", status, verr.Hint)
		}
	}
}
