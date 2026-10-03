package main

import (
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// A refusal the server itself gave names the server as the reader reaches it
// again, and the listing it offers reaches that server too. Through a
// profile's forward the endpoint is 127.0.0.1 and a port that closed with the
// call, so "127.0.0.1:41233 rejected the credentials" named nothing the
// reader could change, and the listing named no profile at all: pasted, it
// listed whatever the configuration there named.
func TestAnAnsweredRefusalNamesTheProfileAndItsListingReachesTheServer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     minio.ErrorResponse
		tunnel  plugin.Tunnel
		profile string
		surface plugin.Surface
		message []string
		hint    []string
		unhint  []string
	}{
		{"credentials rejected through a forward", minio.ErrorResponse{Code: minio.InvalidAccessKeyID},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			[]string{"profile prod (through its kube: forward) rejected the credentials"}, nil, nil},
		{"credentials rejected reached directly", minio.ErrorResponse{Code: minio.SignatureDoesNotMatch},
			plugin.TunnelNone, "prod", plugin.SurfaceCLI,
			[]string{"s3.internal:9000 (profile prod) rejected the credentials"}, nil, nil},
		{"access denied through a forward", minio.ErrorResponse{Code: minio.AccessDenied, Message: "nope"},
			plugin.TunnelSSH, "prod", plugin.SurfaceCLI,
			[]string{"profile prod (through its ssh: forward) refused: nope"}, nil, nil},
		{"a bucket that is not there, at a terminal", minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: "shop"},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI,
			[]string{`profile prod (through its kube: forward) has no bucket "shop"`},
			[]string{"`rta s3 bucket list --profile prod`"}, []string{"--endpoint"}},
		{"a bucket that is not there, to an agent", minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: "shop"},
			plugin.TunnelKube, "prod", plugin.SurfaceMCP,
			nil, []string{"`s3_bucket_list {\"profile\":\"prod\"}`"}, []string{"endpoint"}},
		{"an object that is not there, reached directly", minio.ErrorResponse{Code: minio.NoSuchKey, BucketName: "shop", Key: "k"},
			plugin.TunnelNone, "prod", plugin.SurfaceCLI, nil,
			[]string{"`rta s3 object list --bucket shop --profile prod --endpoint s3.internal:9000`"}, nil},
		{"a bucket that exists", minio.ErrorResponse{Code: minio.BucketAlreadyExists, BucketName: "shop"},
			plugin.TunnelKube, "prod", plugin.SurfaceCLI, nil,
			[]string{"`rta s3 bucket list --profile prod`"}, []string{"--endpoint"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "s3.object.get", map[string]any{"key": "k", "endpoint": "s3.internal:9000"}).
				WithProfile(tc.profile, tc.tunnel).WithSurface(tc.surface)
			verr := classify(tc.err, r)
			for _, w := range tc.message {
				if !strings.Contains(verr.Message, w) {
					t.Errorf("message %q does not hold %q", verr.Message, w)
				}
			}
			for _, w := range tc.hint {
				if !strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q does not hold %q", verr.Hint, w)
				}
			}
			for _, w := range tc.unhint {
				if strings.Contains(verr.Hint, w) {
					t.Errorf("hint %q holds %q", verr.Hint, w)
				}
			}
		})
	}
}
