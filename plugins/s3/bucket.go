package main

import (
	"context"
	"slices"
	"sort"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func bucketField(help string) plugin.Field {
	return plugin.Field{Name: "bucket", Short: "b", Type: plugin.String, Required: true, Config: "bucket", Help: help,
		Live: true, Suggest: suggestBuckets}
}

// optionalBucketField is the bucket of a listing: a list of the buckets it is
// about, and every bucket the key sees when it names none.
//
// It exists so that a profile's `only: bucket:` has something to hold the
// listing to: a connection fenced to its application's bucket answered
// s3.overview and s3.bucket.list with every bucket of the account, names the
// fence was written to keep out. Declared with the fence's own Config key, a
// one-entry fence fills it, and a call that names another bucket is refused.
//
// **A list, because it filters the set of buckets the connection holds.** It
// was one name, so a profile fenced to the several buckets of an application
// could not be handed to either listing: the host refused a call that named
// none, and one that named a list read as text that is in no fence entry. A
// bucket the other capabilities act on (bucketField) stays one name, since
// what they do is to one bucket and its keys, and a grant's scope is a key in
// that bucket.
//
// No Live completion, which is for a String box only: a list is typed a name
// at a time, and a completion that read the account on every keystroke of it is
// the channel Live exists to keep a keystroke off.
func optionalBucketField(help string) plugin.Field {
	return plugin.Field{Name: "bucket", Short: "b", Type: plugin.StringSlice, Config: "bucket", Help: help}
}

// bucketNames is the buckets a listing was asked about, once each and in
// order: an empty string is no name and a name given twice is one. Sorted, so
// that the same list is the same page whichever way it was written.
//
// A name is used exactly as the host held it to the profile's fence, which
// compares the raw element: a name trimmed here would be one the fence never
// saw, and the only thing a trim could then do is read a bucket the fence did
// not allow.
func bucketNames(req plugin.Request) []string {
	var names []string
	for _, name := range req.StringSlice("bucket") {
		if name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// boundBucketField is bucketField's Local sibling, for every capability
// whose grant Scope is "key": the bucket is the container the record sits
// in, and internal/grant's scopes() reads only the inputs a capability names
// as its scope, so a caller-settable bucket would let a grant on one key's
// scope authorize the identical key name in a bucket the grant never named —
// the same hole copy.go's dest-bucket is already Local to close, one level in
// from the destination side.
//
// What an agent is told about it is boundBucketNote, because the schema says
// nothing: a Local input is in no tool's schema, and the bridge drops one an
// agent sends. Without the sentence, s3.object.list on one bucket and
// s3.object.get on the next read as the same bucket, and a key found in one
// is fetched from the operator's — the call succeeds, on the wrong object, or
// refuses with a name that is in the listing the agent just read.
const boundBucketNote = "The bucket is the operator's `bucket` setting: an agent cannot name one, and " +
	"the \"bucket\" argument of s3.object.list does not carry over to this call."

// boundBucketAgentNote is boundBucketNote as an agent reads it: the schema it
// is shown has no bucket input to name, so the note cannot point at one.
const boundBucketAgentNote = "The bucket is the operator's setting, not one you name: the bucket the list " +
	"capability took does not carry over to this call."

func boundBucketField(help string) plugin.Field {
	f := bucketField(help)
	f.Local = true
	return f
}

func bucketListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "s3.bucket.list",
		Summary:    "List every bucket the configured credentials can see",
		Keywords:   []string{"ls", "storage", "minio", "stores"},
		Safety:     plugin.Read,
		Idempotent: true,
		Run:        runBucketList,
		Description: "Every bucket the credentials can see, with age, and region where the server reports one. " +
			"`bucket` is a list: the buckets named, or the ones a profile fences with `only: bucket:`, and no others. " +
			"A name the credentials do not see is a row that says it was not found. A list goes in the call, " +
			"a tile's `with:` or that fence: the config's `bucket` and a profile's `set:` take one name, " +
			"shared with the capabilities that act on one bucket.",
	}, optionalBucketField("only these buckets — every bucket the credentials can see when omitted"))
}

func runBucketList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(ctx, req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		buckets, err := client.ListBuckets(ctx)
		if err != nil {
			return nil, classify(err, req)
		}
		named := bucketNames(req)
		if len(named) == 0 {
			return bucketTable(buckets), nil
		}
		buckets = slices.DeleteFunc(buckets, func(b minio.BucketInfo) bool { return !slices.Contains(named, b.Name) })
		t := bucketTable(buckets)
		// One name is the listing it always was: no row, and a sentence for the
		// table that has none.
		if len(named) == 1 {
			t.Empty = "no bucket named " + quoted(named[0])
			return t, nil
		}
		for _, name := range named {
			if !slices.ContainsFunc(buckets, func(b minio.BucketInfo) bool { return b.Name == name }) {
				t.Rows = append(t.Rows, notFoundRow(t.Columns, name))
			}
		}
		t.Total = len(t.Rows)
		return t, nil
	})
}

// notFoundRow is the row of a bucket that was named and is not among the ones
// the credentials see: its name, and in the last column, which is the date the
// others have, that it was not found. A listing that quietly left it out reads
// as a smaller account, and not as a name that matched nothing.
func notFoundRow(columns []view.Column, name string) []string {
	row := make([]string, len(columns))
	row[0], row[len(row)-1] = plugin.ListedName(name), "not found"
	return row
}

// bucketTable is the listing s3.bucket.list answers with and s3.overview's
// detail carries, one table so the two cannot show a bucket two ways. Bucket
// names are the server's to accept, and a lenient one accepts what AWS does
// not: a name holding a newline or a bidirectional override reaches here.
func bucketTable(buckets []minio.BucketInfo) view.Table {
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	// Only when a bucket reports one: AWS does, and RustFS and SeaweedFS leave
	// it blank on every bucket, so a column of empty cells under a heading is
	// what those would otherwise get.
	withRegion := slices.ContainsFunc(buckets, func(b minio.BucketInfo) bool { return b.BucketRegion != "" })
	t := view.Table{Columns: []view.Column{{Name: "Name"}}}
	if withRegion {
		t.Columns = append(t.Columns, view.Column{Name: "Region"})
	}
	t.Columns = append(t.Columns, view.Column{Name: "Created"})
	for _, b := range buckets {
		row := []string{plugin.ListedName(b.Name)}
		if withRegion {
			row = append(row, b.BucketRegion)
		}
		t.Rows = append(t.Rows, append(row, b.CreationDate.Format("2006-01-02")))
	}
	t.Total = len(t.Rows)
	t.Empty = "no buckets"
	return t
}

// s3.policy.get is Read: the policy document names what is already
// public or shared, which is the point of being able to look at it — the
// same framing vault.policy.get uses for a policy's rules text, never a
// secret value.
func policyGetCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "s3.policy.get",
		Summary:    "Show a bucket's policy document",
		Keywords:   []string{"acl", "permissions", "iam", "access", "json"},
		Safety:     plugin.Read,
		Idempotent: true,
		Run:        runPolicyGet,
	}, bucketField("bucket to inspect"))
}

// noPolicy is the answer for a bucket with no policy, wherever it is noticed.
// minio-go turns the server's NoSuchBucketPolicy into an empty string and no
// error, so the empty policy is the path every real call takes and the
// classified error was only ever the other one: the answer carried no hint,
// and an agent told a bucket "has no bucket policy set" read it as a bucket
// anybody can reach, or nobody can.
func noPolicy(bucket string) *view.Error {
	return view.Errorf("s3.policy.notfound", "%s has no bucket policy set", quoted(bucket)).
		WithHint("an absent policy is not the same as a deny-all one — access still follows IAM/bucket ACLs")
}

func runPolicyGet(ctx context.Context, req plugin.Request) (view.View, error) {
	return withClient(ctx, req, func(ctx context.Context, client *minio.Client) (view.View, error) {
		policy, err := client.GetBucketPolicy(ctx, req.String("bucket"))
		if err != nil {
			return nil, classify(err, req)
		}
		if policy == "" {
			return nil, noPolicy(req.String("bucket"))
		}
		return view.Text{Body: policy}, nil
	})
}
