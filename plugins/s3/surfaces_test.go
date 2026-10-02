package main

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/minio/minio-go/v7"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// What the plugin says names a capability and an input the way the surface
// reading it gives them: at a terminal as it always read, and elsewhere as a
// tool and its arguments, a form's box, or the connection setting the
// operator holds — never as a flag or an `rta` command line its reader has no
// terminal for, except in the one phrase that hands a command to the
// operator.
func TestWhatItSaysNamesWhatItsSurfaceGives(t *testing.T) {
	refusal := func(verr *view.Error) string { return verr.Message + "\n" + verr.Hint }
	r := func(sf plugin.Surface) plugin.Request {
		return req(t, "s3.object.get", map[string]any{"key": "k", "endpoint": "s3.internal:9000"}).WithSurface(sf)
	}
	caRefusal := func(sf plugin.Surface, ca string) string {
		_, verr := connect(req(t, "s3.overview", map[string]any{"ca-file": ca}).WithSurface(sf))
		if verr == nil {
			t.Fatalf("a ca-file of %s was accepted", ca)
		}
		return refusal(verr)
	}
	for _, tc := range []struct {
		name, cli, other string
		surface          plugin.Surface
		say              func(sf plugin.Surface) string
	}{
		{
			name:    "a CA file that cannot be read",
			cli:     "--ca-file is a path on this machine",
			other:   "the operator's `ca-file` setting is a path on this machine",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return caRefusal(sf, filepath.Join(t.TempDir(), "absent.pem"))
			},
		},
		{
			// What the file must hold, never a refusal calling a self-signed
			// server's own certificate the wrong file: it is the file the
			// untrusted-certificate hint sends the reader to name.
			name:    "a CA file with no PEM certificate in it",
			cli:     "--ca-file wants a PEM certificate — the CA's, or a self-signed server's own",
			other:   "the operator's `ca-file` setting wants a PEM certificate — the CA's, or a self-signed server's own",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				der := filepath.Join(t.TempDir(), "public.der")
				if err := os.WriteFile(der, []byte{0x30, 0x03, 0x02, 0x01, 0x01}, 0o600); err != nil {
					t.Fatal(err)
				}
				return caRefusal(sf, der)
			},
		},
		{
			name:    "a bucket that is not there",
			cli:     "`rta s3 bucket list` shows what is there",
			other:   "the `s3_bucket_list` tool shows what is there",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(minio.ErrorResponse{Code: minio.NoSuchBucket, BucketName: "shop"}, r(sf)))
			},
		},
		{
			name:    "credentials the server rejected",
			cli:     "or check --access-key",
			other:   "or check the operator's `access-key` setting",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(minio.ErrorResponse{Code: minio.InvalidAccessKeyID}, r(sf)))
			},
		},
		{
			name:    "nothing listening",
			cli:     "is the server up, and is --endpoint right?",
			other:   "is the server up, and is the operator's `endpoint` setting right?",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, r(sf)))
			},
		},
		{
			name:    "a name DNS does not know",
			cli:     "no address for \"s3.internal\"\n`rta net dns s3.internal` shows what DNS returns",
			other:   "`net_dns {\"name\":\"s3.internal\"}` shows what DNS returns",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(&net.OpError{Op: "dial", Net: "tcp",
					Err: &net.DNSError{Err: "no such host", Name: "s3.internal"}}, r(sf)))
			},
		},
		{
			name:    "a certificate nothing trusts",
			cli:     "the CA that issued it belongs in --ca-file (a self-signed certificate is its own CA)",
			other:   "the CA that issued it belongs in the operator's `ca-file` setting (a self-signed certificate is its own CA)",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(x509.UnknownAuthorityError{}, r(sf)))
			},
		},
		{
			name:    "anything else",
			cli:     "`rta explain s3.overview` lists every input and which of the command line, the rta config, a profile and the environment can set it",
			other:   "ask the operator to run `rta explain s3.overview`, which lists every setting and which of the rta config",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return refusal(classify(errors.New("handshake went sideways"), r(sf)))
			},
		},
		{
			name:    "an object too large to print",
			cli:     "use --out to write it to a file instead of printing it",
			other:   "ask the operator to run `rta s3 object get 'report q1.csv' --bucket shop --out <file>`",
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return outHint(r(sf), "shop", "report q1.csv")
			},
		},
		{
			name:    "a destination already taken",
			cli:     "rta s3 object rm reports/q1.csv --bucket shop --endpoint s3.internal:9000",
			other:   `s3_object_rm {"key":"reports/q1.csv"}`,
			surface: plugin.SurfaceMCP,
			say: func(sf plugin.Surface) string {
				return rmCall(r(sf), "shop", "reports/q1.csv")
			},
		},
		{
			name:    "nothing to upload",
			cli:     "give <value>, or --file to upload from disk",
			other:   `give the "value" argument`,
			surface: plugin.SurfaceMCP,
			say:     noValueHint,
		},
		{
			name:    "nothing to upload, in a form",
			cli:     "give <value>, or --file to upload from disk",
			other:   "give the value box, or the file box to upload from disk",
			surface: plugin.SurfaceTUI,
			say:     noValueHint,
		},
		{
			name:    "a directory that is not there",
			cli:     "`rta s3 bucket download --out <dir>` writes one",
			other:   "`s3.bucket.download out=<dir>` writes one",
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				_, _, verr := planUpload(sf, filepath.Join(t.TempDir(), "nope"), 10)
				return refusal(verr)
			},
		},
		{
			// The directory offered is named after the bucket as it was typed,
			// and on a command line it is one word whatever that holds.
			name:    "a download with nowhere to go",
			cli:     "--out './shop $(id)-backup' — a bucket is a directory of files",
			other:   `the out box set to "./shop $(id)-backup" — a bucket is a directory of files`,
			surface: plugin.SurfaceTUI,
			say: func(sf plugin.Surface) string {
				_, err := runBucketDownload(context.Background(), req(t, "s3.bucket.download",
					map[string]any{"bucket": "shop $(id)"}).WithSurface(sf))
				var verr *view.Error
				if !errors.As(err, &verr) {
					t.Fatalf("err = %v, want a refusal", err)
				}
				return refusal(verr)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if said := tc.say(plugin.SurfaceCLI); !strings.Contains(said, tc.cli) {
				t.Errorf("the CLI reads %q, want %q in it", said, tc.cli)
			}
			said := tc.say(tc.surface)
			if !strings.Contains(said, tc.other) {
				t.Errorf("%s reads %q, want %q in it", tc.surface, said, tc.other)
			}
			if strings.Contains(said, tc.cli) {
				t.Errorf("%s reads the CLI's %q", tc.surface, tc.cli)
			}
		})
	}
}

// The removal a taken destination offers runs against the server the copy
// reached, never whatever the configuration where it is pasted points at:
// through a profile it names the profile, whose credentials the copy may have
// used; reached directly, the endpoint too, which may be one typed over the
// profile's, and how it was reached when that was protected; through a
// forward the host opened, the profile alone, since the endpoint was the
// forward's end on 127.0.0.1. An agent gives the profile and nothing Local.
func TestTheRemovalOfATakenDestinationReachesTheServerTheCopyReached(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]any
		profile string
		tunnel  plugin.Tunnel
		sf      plugin.Surface
		want    string
	}{
		{"no profile", map[string]any{"endpoint": "s3.internal:9000"}, "", plugin.TunnelNone, plugin.SurfaceCLI,
			"rta s3 object rm reports/q1.csv --bucket shop --endpoint s3.internal:9000"},
		{"a profile reached directly", map[string]any{"endpoint": "s3.internal:9000"}, "prod", plugin.TunnelNone,
			plugin.SurfaceCLI, "rta s3 object rm reports/q1.csv --bucket shop --profile prod --endpoint s3.internal:9000"},
		{"a profile through a forward", map[string]any{"endpoint": "127.0.0.1:54321"}, "prod", plugin.TunnelKube,
			plugin.SurfaceCLI, "rta s3 object rm reports/q1.csv --bucket shop --profile prod"},
		{"over TLS", map[string]any{"endpoint": "s3.internal:9000", "tls": true, "ca-file": "~/ca.pem",
			"tls-server-name": "minio.svc"}, "", plugin.TunnelNone, plugin.SurfaceCLI,
			"rta s3 object rm reports/q1.csv --bucket shop --endpoint s3.internal:9000 --tls " +
				"--ca-file '~/ca.pem' --tls-server-name minio.svc"},
		{"an agent through a profile", map[string]any{"endpoint": "s3.internal:9000", "tls": true}, "prod",
			plugin.TunnelNone, plugin.SurfaceMCP, `s3_object_rm {"key":"reports/q1.csv","profile":"prod"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := req(t, "s3.object.copy", tc.values).WithProfile(tc.profile, tc.tunnel).WithSurface(tc.sf)
			if got := rmCall(r, "shop", "reports/q1.csv"); got != tc.want {
				t.Errorf("removal = %q, want %q", got, tc.want)
			}
		})
	}
}
