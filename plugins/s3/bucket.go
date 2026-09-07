package main

import (
	"context"
	"sort"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func bucketField(help string) plugin.Field {
	return plugin.Field{Name: "bucket", Type: plugin.String, Required: true, Config: "bucket", Help: help,
		Live: true, Suggest: suggestBuckets}
}

// boundBucketField is bucketField's Local sibling, for every capability
// whose grant Scope is "key": the bucket is the container the record sits
// in, and internal/grant's scopes() reads only the scoped field, so a
// caller-settable bucket would let a grant on one key's scope authorize the
// identical key name in a bucket the grant never named — the same hole
// copy.go's dest-bucket is already Local to close, one level in from the
// destination side.
func boundBucketField(help string) plugin.Field {
	f := bucketField(help)
	f.Local = true
	return f
}

func bucketListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "s3.bucket.list",
		Summary:    "List every bucket the configured credentials can see",
		Safety:     plugin.Read,
		Idempotent: true,
		Run:        runBucketList,
	})
}

func runBucketList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		buckets, err := client.ListBuckets(ctx)
		if err != nil {
			return nil, classify(err, req)
		}
		sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
		t := view.Table{Columns: []view.Column{{Name: "Name"}, {Name: "Region"}, {Name: "Created"}}}
		for _, b := range buckets {
			t.Rows = append(t.Rows, []string{b.Name, b.BucketRegion, b.CreationDate.Format("2006-01-02")})
		}
		t.Total = len(t.Rows)
		return t, nil
	})
}

// s3.policy.get is Read: the policy document names what is already
// public or shared, which is the point of being able to look at it — the
// same framing vault.policy.get uses for a policy's rules text, never a
// secret value.
func policyGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "s3.policy.get",
		Summary:    "Show a bucket's policy document",
		Safety:     plugin.Read,
		Idempotent: true,
		Run:        runPolicyGet,
	}, bucketField("bucket to inspect"))
}

func runPolicyGet(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		policy, err := client.GetBucketPolicy(ctx, req.String("bucket"))
		if err != nil {
			return nil, classify(err, req)
		}
		if policy == "" {
			return nil, view.Errorf("s3.policy.notfound", "%q has no bucket policy set", req.String("bucket"))
		}
		return view.Text{Body: policy}, nil
	})
}
