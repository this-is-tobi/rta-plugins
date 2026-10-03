package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// minio-go answers a bucket with no policy as an empty string and no error,
// so that is the path a real call takes — and it returned the refusal with no
// hint, where the classified error that nothing reaches carried one. A bucket
// "has no bucket policy set" says nothing about who may reach it, and an agent
// read the silence as one answer or the other.
func TestABucketWithNoPolicyIsToldWhatThatDoesNotMean(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchBucketPolicy</Code>` +
			`<Message>The bucket policy does not exist</Message><BucketName>shop</BucketName></Error>`))
	}))
	defer srv.Close()

	_, err := runPolicyGet(t.Context(), reqFor(t, "s3.policy.get", endpointOf(t, srv), map[string]any{"bucket": "shop"}))
	var verr *view.Error
	if !errors.As(err, &verr) || verr.Code != "s3.policy.notfound" {
		t.Fatalf("err = %v, want s3.policy.notfound", err)
	}
	if !strings.Contains(verr.Hint, "not the same as a deny-all one") {
		t.Errorf("hint = %q, want it to say what an absent policy does not mean", verr.Hint)
	}
}
