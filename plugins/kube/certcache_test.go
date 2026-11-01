package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// A cluster whose kubectl is a script: it logs every call, answers `config
// view` from a file the test can rewrite, answers a Secret list from another,
// a `get --raw <path>` from the file of that path (rawAt), and every other list
// with nothing. What these tests count is the Secret
// lists, because each one brings every TLS Secret's private key across the wire
// and is a line in the API server's audit log.
//
// A list named for one namespace (--namespace=x) is answered from that
// namespace's own file when the test wrote one (listIn, secretsIn, failIn) and
// from the cluster's general one when it did not, so a cluster scripted as one
// list keeps answering every namespace with the same items. A cluster that
// refuses every list across namespaces (forbidClusterWide) is the namespaced
// credential, the ordinary restricted identity; the file config.fail makes
// `config view` fail with the words in it; the file conc.slow makes every
// get take a moment and writes how many were running to conc, which is how a
// test sees the bound on concurrent calls.
type scriptedCluster struct{ dir string }

func newScriptedCluster(t *testing.T) *scriptedCluster {
	t.Helper()
	c := &scriptedCluster{dir: t.TempDir()}
	dir := shellQuote(c.dir)
	script := `#!/bin/sh
echo "$*" >> ` + shellQuote(filepath.Join(c.dir, "calls")) + `
ns=""
all=""
for a in "$@"; do
	case "$a" in
	--namespace=*) ns="${a#--namespace=}" ;;
	--all-namespaces) all=1 ;;
	esac
done
if [ "$1" = get ] && [ -f ` + dir + `/conc.slow ]; then
	echo up >> ` + dir + `/conc
	sleep 0.2
	echo down >> ` + dir + `/conc
fi
if [ "$1" = get ] && [ -f ` + shellQuote(filepath.Join(c.dir, "get.fail")) + ` ]; then
	cat ` + shellQuote(filepath.Join(c.dir, "get.fail")) + ` >&2
	exit 1
fi
if [ "$1" = get ] && [ -n "$all" ] && [ -f ` + dir + `/forbid-clusterwide ]; then
	echo "Error from server (Forbidden): $2 is forbidden: User \"system:serviceaccount:ops:rta\" cannot list resource \"$2\" in API group \"\" at the cluster scope" >&2
	exit 1
fi
if [ "$1 $2" = "config view" ] && [ -f ` + shellQuote(filepath.Join(c.dir, "config.fail")) + ` ]; then
	cat ` + shellQuote(filepath.Join(c.dir, "config.fail")) + ` >&2
	exit 1
fi
case "$1 $2" in
"config view") cat ` + shellQuote(filepath.Join(c.dir, "config.json")) + ` ;;
"get --raw")
	if [ -f ` + dir + `/list---raw.fail ]; then
		cat ` + dir + `/list---raw.fail >&2
		exit 1
	fi
	case "$3" in
	*/namespaces/*) ;;
	*)
		if [ -f ` + dir + `/forbid-clusterwide ]; then
			echo "Error from server (Forbidden): pods.metrics.k8s.io is forbidden: User \"system:serviceaccount:ops:rta\" cannot list resource \"pods\" in API group \"metrics.k8s.io\" at the cluster scope" >&2
			exit 1
		fi ;;
	esac
	key=$(printf %s "$3" | tr / _)
	if [ -f ` + dir + `/raw"$key".fail ]; then
		cat ` + dir + `/raw"$key".fail >&2
		exit 1
	fi
	if [ -f ` + dir + `/raw"$key".json ]; then cat ` + dir + `/raw"$key".json; exit 0; fi
	echo "Error from server (NotFound): the server could not find the requested resource" >&2
	exit 1 ;;
"get secrets")
	if [ -f ` + shellQuote(filepath.Join(c.dir, "secrets.slow")) + ` ]; then sleep 1; fi
	if [ -n "$ns" ] && [ -f ` + dir + `/secrets."$ns".fail ]; then
		cat ` + dir + `/secrets."$ns".fail >&2
		exit 1
	fi
	if [ -f ` + shellQuote(filepath.Join(c.dir, "secrets.fail")) + ` ]; then
		cat ` + shellQuote(filepath.Join(c.dir, "secrets.fail")) + ` >&2
		exit 1
	fi
	if [ -n "$ns" ] && [ -f ` + dir + `/secrets."$ns".json ]; then cat ` + dir + `/secrets."$ns".json; exit 0; fi
	cat ` + shellQuote(filepath.Join(c.dir, "secrets.json")) + ` ;;
"get "*)
	if [ -n "$ns" ] && [ -f ` + dir + `/list-"$2"."$ns".fail ]; then
		cat ` + dir + `/list-"$2"."$ns".fail >&2
		exit 1
	fi
	if [ -f ` + dir + `/list-"$2".fail ]; then
		cat ` + dir + `/list-"$2".fail >&2
		exit 1
	fi
	if [ -n "$ns" ] && [ -f ` + dir + `/list-"$2"."$ns".json ]; then cat ` + dir + `/list-"$2"."$ns".json; exit 0; fi
	if [ -f ` + dir + `/list-"$2".json ]; then cat ` + dir + `/list-"$2".json; else echo '{"items":[]}'; fi ;;
*) echo '{"items":[]}' ;;
esac
`
	path := filepath.Join(c.dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := kubectlBin
	kubectlBin = path
	resetCertReads()
	t.Cleanup(func() { kubectlBin = orig; resetCertReads() })
	c.currentContext("prod-eu", "")
	c.secrets(nil)
	return c
}

func (c *scriptedCluster) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (c *scriptedCluster) currentContext(name, namespace string) {
	body := fmt.Sprintf(`{"current-context":%q,"contexts":[{"name":%q,"context":{"cluster":"c","user":"u","namespace":%q}},`+
		`{"name":"staging","context":{"cluster":"c","user":"u"}}],"clusters":[{"name":"c","cluster":{"server":"https://10.0.0.1"}}]}`,
		name, name, namespace)
	if err := os.WriteFile(filepath.Join(c.dir, "config.json"), []byte(body), 0o600); err != nil {
		panic(err)
	}
}

// secrets sets the TLS Secrets the cluster holds: namespace/name -> tls.crt.
func (c *scriptedCluster) secrets(items []string) {
	body := `{"items":[` + strings.Join(items, ",") + `]}`
	if err := os.WriteFile(filepath.Join(c.dir, "secrets.json"), []byte(body), 0o600); err != nil {
		panic(err)
	}
}

func secretItem(namespace, name, crt, key string) string {
	return `{"metadata":{"name":"` + name + `","namespace":"` + namespace + `"},"data":{"tls.crt":"` + crt +
		`","tls.key":"` + base64.StdEncoding.EncodeToString([]byte(key)) + `"}}`
}

// list sets what the cluster answers to `get <resource>`: the items of the
// list, as JSON objects.
func (c *scriptedCluster) list(t *testing.T, resource string, items ...string) {
	t.Helper()
	c.write(t, "list-"+resource+".json", `{"items":[`+strings.Join(items, ",")+`]}`)
}

func (c *scriptedCluster) calls(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.dir, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func (c *scriptedCluster) secretLists(t *testing.T) []string {
	var out []string
	for _, line := range c.calls(t) {
		if strings.HasPrefix(line, "get secrets") {
			out = append(out, line)
		}
	}
	return out
}

func certListRun(t *testing.T, values map[string]any) view.View {
	t.Helper()
	v, err := runCertList(context.Background(), saReq(plugin.SurfaceCLI, false, values))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func resetCertReads() {
	certReads.Lock()
	defer certReads.Unlock()
	certReads.byScope = map[certScope]*certEntry{}
}

// ageCertReads makes every kept read look d older, so a test can cross the
// cache's lifetime without waiting for it.
func ageCertReads(d time.Duration) {
	certReads.Lock()
	defer certReads.Unlock()
	for _, e := range certReads.byScope {
		e.at = e.at.Add(-d)
	}
}

func TestTheCertificatesAreReadOnceInTheirLifetime(t *testing.T) {
	c := newScriptedCluster(t)
	c.secrets([]string{secretItem("prod", "web-tls", selfSignedPEM(t, "web", time.Now().Add(90*24*time.Hour)), "KEY")})

	first := fmt.Sprintf("%#v", certListRun(t, map[string]any{"namespace": "prod"}))
	again := fmt.Sprintf("%#v", certListRun(t, map[string]any{"namespace": "prod"}))
	if n := len(c.secretLists(t)); n != 1 {
		t.Fatalf("two lists inside the lifetime made %d Secret reads, want 1:\n%s", n, strings.Join(c.calls(t), "\n"))
	}
	if !strings.Contains(first, "web") || !strings.Contains(again, "web") {
		t.Errorf("the certificate is missing from an answer:\n%s\n%s", first, again)
	}

	ageCertReads(certLife - time.Minute)
	certListRun(t, map[string]any{"namespace": "prod"})
	if n := len(c.secretLists(t)); n != 1 {
		t.Errorf("a read a minute inside its lifetime was made again (%d Secret reads)", n)
	}
	ageCertReads(2 * time.Minute)
	certListRun(t, map[string]any{"namespace": "prod"})
	if n := len(c.secretLists(t)); n != 2 {
		t.Errorf("a read past its lifetime was reused (%d Secret reads, want 2)", n)
	}
}

// The overview and the listing are one reader of the cluster's certificates:
// a tile of each, which is the point of keeping the read at all, costs one
// Secret list between them.
func TestTheOverviewAndTheCertListingShareOneRead(t *testing.T) {
	c := newScriptedCluster(t)
	c.secrets([]string{secretItem("prod", "web-tls", selfSignedPEM(t, "web", time.Now().Add(-time.Hour)), "KEY")})

	v, err := runOverview(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	if body := fmt.Sprintf("%#v", v); !strings.Contains(body, "prod/web-tls") {
		t.Fatalf("the overview does not report the expired certificate:\n%s", body)
	}
	certListRun(t, map[string]any{"all-namespaces": true})
	if n := len(c.secretLists(t)); n != 1 {
		t.Errorf("the overview and the listing made %d Secret reads, want 1:\n%s", n, strings.Join(c.calls(t), "\n"))
	}
}

// Two tiles that fall due on the same tick are one read, not two: the second
// waits for the first, and finds the answer kept.
func TestReadersThatArriveTogetherMakeOneRead(t *testing.T) {
	c := newScriptedCluster(t)
	c.secrets([]string{secretItem("prod", "web-tls", selfSignedPEM(t, "web", time.Now().Add(90*24*time.Hour)), "KEY")})
	c.write(t, "secrets.slow", "")

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runCertList(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"namespace": "prod"})); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(c.secretLists(t)); n != 1 {
		t.Errorf("four readers at once made %d Secret reads, want 1", n)
	}
}

func TestACertificateReadIsKeptPerContextAndPerNamespace(t *testing.T) {
	c := newScriptedCluster(t)
	c.secrets([]string{secretItem("prod", "web-tls", selfSignedPEM(t, "web", time.Now().Add(90*24*time.Hour)), "KEY")})

	certListRun(t, map[string]any{"namespace": "prod"})
	certListRun(t, map[string]any{"namespace": "other"})
	certListRun(t, map[string]any{"all-namespaces": true})
	certListRun(t, map[string]any{"namespace": "prod", "context": "staging"})
	if n := len(c.secretLists(t)); n != 4 {
		t.Fatalf("four scopes made %d Secret reads, want 4:\n%s", n, strings.Join(c.calls(t), "\n"))
	}

	// The current context moves under a caller that named none: the answer of
	// the cluster it pointed at must not be served for the one it points at now.
	c.currentContext("staging-2", "")
	certListRun(t, map[string]any{"namespace": "prod"})
	if n := len(c.secretLists(t)); n != 5 {
		t.Errorf("a changed current context was served from the old one's read (%d Secret reads, want 5)", n)
	}
}

// Resolving the context to key the read and then reading under whichever one is
// current at that moment would cache one cluster's certificates under another's
// name, so the read is made against the context it was keyed by.
func TestACertificateReadIsMadeAgainstTheContextItIsKeptUnder(t *testing.T) {
	c := newScriptedCluster(t)
	c.currentContext("prod-eu", "payments")

	certListRun(t, map[string]any{})
	lists := c.secretLists(t)
	if len(lists) != 1 {
		t.Fatalf("%d Secret reads, want 1", len(lists))
	}
	for _, want := range []string{"--context=prod-eu", "--namespace=payments"} {
		if !strings.Contains(lists[0], want) {
			t.Errorf("the read %q does not carry %s", lists[0], want)
		}
	}
}

func TestAFailedCertificateReadIsNotKept(t *testing.T) {
	c := newScriptedCluster(t)
	c.write(t, "secrets.fail", `Error from server (Forbidden): secrets is forbidden: User "u" cannot list resource "secrets"`)
	if _, err := runCertList(context.Background(), saReq(plugin.SurfaceCLI, false, map[string]any{"namespace": "prod"})); err == nil {
		t.Fatal("a refused read was answered")
	}
	if err := os.Remove(filepath.Join(c.dir, "secrets.fail")); err != nil {
		t.Fatal(err)
	}
	certListRun(t, map[string]any{"namespace": "prod"})
	if n := len(c.secretLists(t)); n != 2 {
		t.Errorf("%d Secret reads after a refusal and a retry, want 2", n)
	}
}

// What is kept of a TLS Secret is what the pages print. Neither the key nor the
// certificate it came with is among it, so a read held for a quarter of an hour
// holds nothing that could be used as a credential or a certificate.
func TestWhatIsKeptOfASecretIsWhatThePagesPrint(t *testing.T) {
	c := newScriptedCluster(t)
	const key = "MARKER-PRIVATE-KEY-MUST-NOT-BE-KEPT"
	crt := selfSignedPEM(t, "kept.example.com", time.Now().Add(90*24*time.Hour))
	c.secrets([]string{secretItem("prod", "web-tls", crt, key)})
	certListRun(t, map[string]any{"namespace": "prod"})

	var kept string
	certReads.Lock()
	for _, e := range certReads.byScope {
		kept += fmt.Sprintf("%#v\n", e.certs)
	}
	certReads.Unlock()
	if !strings.Contains(kept, "kept.example.com") || !strings.Contains(kept, "web-tls") {
		t.Fatalf("the read kept nothing of the certificate:\n%s", kept)
	}
	for _, forbidden := range []string{key, base64.StdEncoding.EncodeToString([]byte(key)), crt} {
		if strings.Contains(kept, forbidden) {
			t.Errorf("the kept read holds %q", forbidden)
		}
	}
}

// The window a certificate is judged against moves with the clock, and the read
// does not: a certificate that expires inside the lifetime of the read is
// expired in the answer served after it.
func TestACertificateIsJudgedAtTheTimeOfTheAnswerNotOfTheRead(t *testing.T) {
	now := time.Now()
	c := tlsCert{NotAfter: now.Add(10 * time.Minute)}
	if got := c.status(now); got != "expiring soon" {
		t.Errorf("status at the read = %q, want expiring soon", got)
	}
	if got := c.status(now.Add(15 * time.Minute)); got != "expired" {
		t.Errorf("status 15 minutes on = %q, want expired", got)
	}
	if got := (tlsCert{Problem: "tls.crt is not valid base64"}).status(now); got != "invalid — tls.crt is not valid base64" {
		t.Errorf("a certificate that did not parse reads %q", got)
	}
}

func TestACachedListingSaysHowOldItIs(t *testing.T) {
	c := newScriptedCluster(t)
	c.secrets([]string{secretItem("prod", "web-tls", selfSignedPEM(t, "web", time.Now().Add(90*24*time.Hour)), "KEY")})

	fresh := certListRun(t, map[string]any{"namespace": "prod"}).(view.Table)
	if len(fresh.Warnings) != 0 {
		t.Errorf("a listing read just now carries a note: %+v", fresh.Warnings)
	}
	if again := certListRun(t, map[string]any{"namespace": "prod"}).(view.Table); len(again.Warnings) != 0 {
		t.Errorf("a listing served from a read a moment old carries a note: %+v", again.Warnings)
	}
	ageCertReads(6 * time.Minute)
	reused := certListRun(t, map[string]any{"namespace": "prod"}).(view.Table)
	if len(reused.Warnings) != 1 || reused.Warnings[0].Code != "kube.cert.reused" ||
		!strings.Contains(reused.Warnings[0].Message, "6m") || !strings.Contains(reused.Warnings[0].Hint, "15m") {
		t.Errorf("a listing served from a read six minutes old says %+v", reused.Warnings)
	}
}
