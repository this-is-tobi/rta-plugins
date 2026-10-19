package main

import (
	"context"
	"sort"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
)

// Live completion: the inputs whose values exist server-side, completed on
// the deliberate channel (Field.Live). Each one is a
// listing — a read, made with the credentials the run would use, visible in
// the provider's own audit trail as the same principal. Silent on every
// failure, like any Suggest: a completion that cannot answer slows nobody
// down, and the run that follows classifies the same failure properly.

// completionCap bounds one listing's answer. A suggestion list is read by a
// person; past a screenful the answer is "type more", and an unbounded walk
// of a million-object bucket is a bill, not a completion.
const completionCap = 60

// suggestBuckets lists what the configured credentials can see — the same
// call s3.bucket.list makes, minus everything but the names.
//
// It serves the bucket of a single-bucket capability and the list of the two
// listings alike, and reads the box of neither: on a list the request carries the
// name being typed under `bucket`, and the answer is every bucket the account has,
// for the box to match against what is typed. The host leaves out the buckets the
// box already lists and those a profile's fence does not allow, so the account is
// listed whole and the fence holds the answer, not the call.
//
// The answer is not capped, as the keys' is (completionCap). That cap is honest because
// the text typed is the prefix the service is asked for, so typing more brings the rest.
// Here the text is never sent: the host narrows what the box offers by it after this has
// answered, so a cap would cut the buckets by their place in the list, and no typing
// could bring a cut one back.
func suggestBuckets(ctx context.Context, req plugin.Request) []string {
	client, verr := connect(req)
	if verr != nil {
		return nil
	}
	buckets, err := client.ListBuckets(ctx)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(buckets))
	for _, b := range buckets {
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}

// suggestKeys walks a bucket one "/" segment at a time: the box's own text
// is the prefix (the host puts it under the field's name — Field.Live), the
// delimiter groups what lies below it, and a common prefix comes back with
// its trailing "/" — which is what lets the next press fetch deeper instead
// of re-accepting (needsFetch's extends-or-fetch rule, the same convention
// the kube coordinate's segments use).
func suggestKeys(field string) func(context.Context, plugin.Request) []string {
	return func(ctx context.Context, req plugin.Request) []string {
		// Read as a list because the overview's bucket is one: a name is a list
		// of one, and the keys of several buckets are no list to complete under.
		buckets := req.StringSlice("bucket")
		if len(buckets) != 1 || buckets[0] == "" {
			return nil // nothing to walk until the sibling names one
		}
		bucket := buckets[0]
		client, verr := connect(req)
		if verr != nil {
			return nil
		}
		// ListObjectsIter, not ListObjects: the iterator form runs the walk
		// in this goroutine, pulled one page at a time by the range loop, so
		// breaking out at the cap is the whole story — there is no feeder
		// goroutine left running against a channel nobody reads any more.
		var out []string
		for obj := range listObjects(ctx, client, bucket, minio.ListObjectsOptions{
			Prefix: req.String(field),
		}) {
			if obj.Err != nil {
				return nil
			}
			out = append(out, obj.Key)
			if len(out) == completionCap {
				break
			}
		}
		return out
	}
}
