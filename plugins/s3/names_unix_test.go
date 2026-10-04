//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/this-is-tobi/rta/pkg/view"
)

// Unix only: a file whose name holds an escape byte or a newline is one a
// Unix filesystem keeps and Windows does not, and the directory a bucket is
// downloaded into or uploaded from is where those names land.

func downloadRefusal(t *testing.T, keys []string, values map[string]any) *view.Error {
	t.Helper()
	srv := serveNames(t, bucketContents{keys: keys, object: []byte("body")})
	values["out"] = filepath.Join(t.TempDir(), "backup")
	values["parallel"] = 1
	_, err := runBucketDownload(context.Background(), downloadReq(t, srv, values))
	var verr *view.Error
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return verr
}

// A key becomes a filename, and the key is the server's: the one that would
// escape the destination is named in the refusal, which is where a name that
// does not draw as itself is the attack and not an accident.
func TestADownloadRefusesAKeyThatDoesNotDrawAsItselfByNamingItQuoted(t *testing.T) {
	verr := downloadRefusal(t, []string{"../x\x1b[31m\n", plainKey, "../" + plainKey, "../blank" + brailleBlank},
		map[string]any{})
	if verr.Code != "s3.download.unsafekey" {
		t.Fatalf("code = %s, want s3.download.unsafekey: %s", verr.Code, verr.Message)
	}
	for _, want := range []string{
		`"../x\x1b[31m\n" (escapes the destination directory)`,
		`"../` + plainKey + `" (escapes the destination directory)`,
		`"../blank` + brailleBlankShown + `" (escapes the destination directory)`,
	} {
		if !strings.Contains(verr.Message, want) {
			t.Errorf("message = %q, want %q in it", verr.Message, want)
		}
	}
	if strings.ContainsAny(verr.Message, "\x1b\n") {
		t.Errorf("message = %q, holds a character a terminal would act on", verr.Message)
	}
}

// Two keys that resolve to one local file, and a key whose directory is a file
// the download wrote a moment before: each names the local path they came to,
// which is built from the key and is the key's name as much as the key is.
func TestADownloadNamesTheLocalFileItCouldNotWriteAsAName(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []string
		code string
		want func(root string) string
	}{
		{"two keys that are one file", []string{"dir\x1b[31m/file", "dir\x1b[31m//file"}, "s3.download.collision",
			func(root string) string {
				return `two object keys resolve to the same local file: "` + root + `/dir\x1b[31m/file"`
			}},
		{"a directory that is a file", []string{"a\x1b", "a\x1b/b"}, "s3.download.create",
			func(root string) string {
				return `creating "` + root + `/a\x1b": mkdir "` + root + `/a\x1b": not a directory`
			}},
		{"two plain keys that are one file", []string{plainKey, "reports//café été.txt"}, "s3.download.collision",
			func(root string) string {
				return "two object keys resolve to the same local file: " + root + "/" + plainKey
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveNames(t, bucketContents{keys: tc.keys, object: []byte("body")})
			root := filepath.Join(t.TempDir(), "backup")
			_, err := runBucketDownload(context.Background(),
				downloadReq(t, srv, map[string]any{"out": root, "parallel": 1}))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if want := tc.want(root); verr.Message != want {
				t.Errorf("message = %q, want %q", verr.Message, want)
			}
		})
	}
}

func TestADownloadReportsWhereItCameFromByAName(t *testing.T) {
	for prefix, want := range map[string]string{
		escKey:              escAddress,
		"reports/café été/": "test-bucket/reports/café été/",
		"":                  "test-bucket/",
		"reports/ ":         `"test-bucket/reports/ "`,
	} {
		srv := serveNames(t, bucketContents{keys: []string{"a.txt"}, object: []byte("body")})
		v, err := runBucketDownload(context.Background(), downloadReq(t, srv,
			map[string]any{"out": filepath.Join(t.TempDir(), "backup"), "prefix": prefix}))
		if err != nil {
			t.Fatal(err)
		}
		var from string
		for _, p := range v.(view.KeyValue).Pairs {
			if p.Key == "from" {
				from = p.Value
			}
		}
		if from != want {
			t.Errorf("prefix %q: from = %q, want %q", prefix, from, want)
		}
	}
}

func TestAnUploadNamesWhatItRefusesAndWhereItWritesAsNames(t *testing.T) {
	t.Run("a symlink named with an escape sequence and a newline", func(t *testing.T) {
		dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
		for _, name := range []string{"link\x1b[31m\nname", "link" + brailleBlank} {
			if err := os.Symlink("a.txt", filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		srv := serveNames(t, bucketContents{})
		_, err := runBucketUpload(context.Background(), uploadReq(t, srv, map[string]any{"dir": dir}))
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "s3.upload.notregular" {
			t.Fatalf("err = %v, want s3.upload.notregular", err)
		}
		for _, want := range []string{
			`"` + dir + `/link\x1b[31m\nname" (a symlink)`,
			`"` + dir + `/link` + brailleBlankShown + `" (a symlink)`,
		} {
			if !strings.Contains(verr.Message, want) {
				t.Errorf("message = %q, want %q in it", verr.Message, want)
			}
		}
	})

	t.Run("a directory it could not read", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a directory with no permissions")
		}
		dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
		unreadable := filepath.Join(dir, "sub\x1b[2J")
		if err := os.Mkdir(unreadable, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) })
		srv := serveNames(t, bucketContents{})
		_, err := runBucketUpload(context.Background(), uploadReq(t, srv, map[string]any{"dir": dir}))
		var verr *view.Error
		if !errors.As(err, &verr) || verr.Code != "s3.upload.walk" {
			t.Fatalf("err = %v, want s3.upload.walk", err)
		}
		if want := "reading " + dir + `: open "` + dir + `/sub\x1b[2J": permission denied`; verr.Message != want {
			t.Errorf("message = %q, want %q", verr.Message, want)
		}
	})

	t.Run("a destination that already holds an object", func(t *testing.T) {
		for key, want := range map[string]string{escKey: escShown, plainKey: plainKey} {
			srv := serveNames(t, bucketContents{keys: []string{key}})
			dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
			_, err := runBucketUpload(context.Background(),
				uploadReq(t, srv, map[string]any{"dir": dir, "prefix": "backup"}))
			var verr *view.Error
			if !errors.As(err, &verr) || verr.Code != "s3.upload.notempty" {
				t.Fatalf("err = %v, want s3.upload.notempty", err)
			}
			if wantMessage := "test-bucket/backup/ already holds objects (" + want + ", and possibly more)"; verr.Message != wantMessage {
				t.Errorf("message = %q, want %q", verr.Message, wantMessage)
			}
		}
	})

	for prefix, want := range map[string]string{
		escKey:              `"test-bucket/esc\x1b[31mred\nline/"`,
		"reports/café été/": "test-bucket/reports/café été/",
		"":                  "test-bucket/",
	} {
		t.Run("under the prefix "+prefix, func(t *testing.T) {
			dir := dirWithFiles(t, map[string]string{"a.txt": "x"})
			values := func() map[string]any {
				return map[string]any{"dir": dir, "prefix": prefix, "overwrite": true}
			}

			srv := serveNames(t, bucketContents{})
			v, err := runBucketUpload(context.Background(), uploadReq(t, srv, values()))
			if err != nil {
				t.Fatal(err)
			}
			var to string
			for _, p := range v.(view.KeyValue).Pairs {
				if p.Key == "to" {
					to = p.Value
				}
			}
			if to != want {
				t.Errorf("to = %q, want %q", to, want)
			}

			previewValues := values()
			previewValues["bucket"] = "test-bucket"
			preview, err := runBucketUpload(context.Background(),
				dryReq(t, "s3.bucket.upload", endpointOf(t, srv), previewValues))
			if err != nil {
				t.Fatal(err)
			}
			if got, wantText := preview.(view.Text).Body, "would upload 1 file (1 B) from "+dir+" into "+want; got != wantText {
				t.Errorf("preview = %q, want %q", got, wantText)
			}

			refused := serveNames(t, bucketContents{refusePut: true})
			_, err = runBucketUpload(context.Background(), uploadReq(t, refused, values()))
			var verr *view.Error
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want the upload's failure", err)
			}
			if !strings.Contains(verr.Hint, want+" may hold a partial upload") {
				t.Errorf("hint = %q, want it to name %s as the prefix that may hold a partial upload", verr.Hint, want)
			}
		})
	}
}
