package main

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"sort"
	"sync"
	"time"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// kube.cert.list: every TLS certificate this cluster stores, and how close
// to expiry it is.
//
// Reads only `type: kubernetes.io/tls` Secrets, selected server-side with
// `--field-selector` rather than fetched-and-filtered — an Opaque secret's
// `data` is API-token and password material with no reason to ever leave
// the API server for this process's memory, let alone transit a JSON
// unmarshal it has no use for. That half holds: a non-TLS secret's data
// genuinely never comes back.
//
// **Within a TLS secret, `tls.key` does arrive, and the struct below does not
// stop it.** This comment used to say the opposite — that the private key "is
// never requested", and that its absence from every struct here was "the
// property under test, not an oversight". That was a category error worth
// naming rather than quietly deleting, because it is an easy one to make
// again: a struct governs what json.Unmarshal *keeps*, never what the API
// server *sends*. Kubernetes has no field projection for a Secret's `data` —
// `--field-selector` chooses which objects come back, never which keys inside
// one — so every selected Secret arrives whole, private key included. Measured
// rather than reasoned about: on a mid-sized cluster that is tens of TLS
// secrets and tens of kilobytes of base64 key material, on every call. Asking
// differently cannot avoid it; only asking less often can.
//
// What is true, and is the property actually worth stating: `tls.key` is never
// decoded, never rendered, never logged and never written anywhere — it is
// dropped at the unmarshal and the response buffer is all that ever held it.
// What is not true is that it stays on the API server. So anything that raises
// how often this runs multiplies how often every private key in the cluster
// crosses the wire into this process — a dashboard tile on a refresh timer
// most of all, which is the tradeoff to weigh before adding one.
//
// **So the read is made at most once in certLife, and shared.** kube.overview
// runs every minute as a tile and reads the same Secrets, and each read is a
// Secret list in the API server's audit log and every key on the wire: sixty an
// hour per open terminal, for a verdict — expired, expiring within a month —
// that moves by the day. The result is kept in this process for a quarter of an
// hour per context and scope, and both capabilities draw on it. What is kept is
// what the pages print (tlsCert): never the key, and not the certificate either.
// The status is worked out from the expiry when an answer is made, not when the
// read was, so a certificate that expires inside the quarter hour is expired in
// the answer that follows it. A read that failed is not kept.
//
// Expiry judgement would ideally be builtin/internal/x509check, the same
// package `cert expiry` and `audit.web` use so two implementations cannot
// disagree about the same certificate's window — but that package sits under
// builtin/internal, and this plugin is a separate Go module entirely (its own
// go.mod, per plugins/kube/kubectl.go's own reasoning for shelling out rather
// than linking a client): an internal package is not reachable across that
// boundary, full stop, regardless of directory nesting. warnDays below is
// x509check.DefaultWarnDays's value, restated rather than shared — if that
// constant ever changes, this should change with it, and there is no
// compiler to enforce that today.

type tlsSecretItem struct {
	Metadata meta `json:"metadata"`
	Data     struct {
		Cert string `json:"tls.crt"`
	} `json:"data"`
}

// tlsCert is what is kept of one TLS Secret: where it lives and what the pages
// print of its leaf certificate. Not the Secret, not tls.crt's bytes and not
// the key, so what the cache holds for a quarter of an hour is a name and a
// date. Problem is set instead of Subject and NotAfter when tls.crt could not be
// read.
type tlsCert struct {
	Namespace string
	Name      string
	Subject   string
	NotAfter  time.Time
	Problem   string
}

// certLife is how long a read of the certificates is reused.
const certLife = 15 * time.Minute

// certScope is what a read covered: the context it was made against, resolved
// to a name, and one namespace or every namespace ("*", which no namespace
// can be named).
type certScope struct{ context, namespace string }

// certEntry is one scope's read. Its mutex is held through the read itself, so
// two callers that find nothing kept (the overview and a listing, started on
// the same tick) make one Secret list between them rather than two.
type certEntry struct {
	mu    sync.Mutex
	at    time.Time
	certs []tlsCert
}

var certReads = struct {
	sync.Mutex
	byScope map[certScope]*certEntry
}{byScope: map[certScope]*certEntry{}}

func certEntryFor(scope certScope) *certEntry {
	certReads.Lock()
	defer certReads.Unlock()
	e, ok := certReads.byScope[scope]
	if !ok {
		e = &certEntry{}
		certReads.byScope[scope] = e
	}
	return e
}

// The kept reads are one for each namespace anyone named, so a plugin that lives
// as long as the server that started it would hold one for every name it was ever
// asked about, and an overview of thirty names is thirty at a time. They are kept
// down in two places, when a read has been kept and when one has failed. What is
// older than certLife is a read nobody would use (forgetOld), and a read that
// failed (a namespace the credential may not list, named or not) holds nothing at
// all (forgetUnread). A caller who had found an entry before it was dropped keeps
// a read of its own that nobody else is given, which costs one read.

// forgetOld drops the kept reads that are older than certLife. An entry in use is
// passed over, by TryLock, so that this never waits for a read in progress: it is
// not old, or it is about to be written, and the caller who holds it is the one
// calling.
func forgetOld() {
	certReads.Lock()
	defer certReads.Unlock()
	for scope, e := range certReads.byScope {
		if !e.mu.TryLock() {
			continue
		}
		old := !e.at.IsZero() && time.Since(e.at) >= certLife
		e.mu.Unlock()
		if old {
			delete(certReads.byScope, scope)
		}
	}
}

// forgetUnread drops the entry of a read that failed, which the caller holds
// the lock of and which has never held a read.
func forgetUnread(scope certScope, e *certEntry) {
	certReads.Lock()
	defer certReads.Unlock()
	if certReads.byScope[scope] == e && e.at.IsZero() {
		delete(certReads.byScope, scope)
	}
}

// certRead is the certificates an answer is made of, and when they were read.
type certRead struct {
	certs []tlsCert
	at    time.Time
	// reused says the read is an earlier one: the answer is not about now.
	reused bool
}

// certScopeOf names what a read of s would cover, and pins s to it. A context
// or a namespace the caller left to kubectl is resolved from the kubeconfig and
// then passed to the read explicitly: resolved first and read under whatever is
// current a moment later, one cluster's certificates could be kept under
// another's name. ok is false when the scope cannot be named, and the read is
// then made, and not kept.
func certScopeOf(ctx context.Context, s selection, cfg *kubeconfig) (certScope, selection, bool) {
	scope := certScope{context: s.Context, namespace: s.Namespace}
	if s.AllNS {
		scope.namespace = "*"
	}
	if scope.context == "" || scope.namespace == "" {
		if cfg == nil {
			read, verr := readConfig(ctx, s.sf)
			if verr != nil {
				return certScope{}, s, false
			}
			cfg = &read
		}
		if scope.context == "" {
			scope.context = cfg.CurrentContext
		}
		if scope.namespace == "" {
			scope.namespace = cfg.namespaceOf(scope.context)
		}
	}
	if scope.context == "" {
		return certScope{}, s, false
	}
	s.Context = scope.context
	if !s.AllNS {
		s.Namespace = scope.namespace
	}
	return scope, s, true
}

// tlsCerts is the certificates of the TLS Secrets s selects, from a read made
// at most certLife ago. It is shared with kube.overview's own composition, so
// both read the same Secrets the same way and draw on the same read. cfg is the
// kubeconfig when the caller has it already, an empty one when the caller asked
// and could not read it (it names no context, so the read is made and not kept,
// and the file is not asked again), and nil when the caller has not asked.
func tlsCerts(ctx context.Context, s selection, cfg *kubeconfig) (certRead, *view.Error) {
	scope, pinned, ok := certScopeOf(ctx, s, cfg)
	if !ok {
		certs, verr := readTLSCerts(ctx, s)
		return certRead{certs: certs, at: time.Now()}, verr
	}
	e := certEntryFor(scope)
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.at.IsZero() && time.Since(e.at) < certLife {
		return certRead{certs: e.certs, at: e.at, reused: true}, nil
	}
	certs, verr := readTLSCerts(ctx, pinned)
	if verr != nil {
		forgetUnread(scope, e)
		return certRead{}, verr
	}
	e.at, e.certs = time.Now(), certs
	forgetOld()
	return certRead{certs: certs, at: e.at}, nil
}

func readTLSCerts(ctx context.Context, s selection) ([]tlsCert, *view.Error) {
	raw, runErr := run(ctx, s.sf, s.args("get", "secrets", "-o", "json",
		"--field-selector=type=kubernetes.io/tls")...)
	if runErr != nil {
		return nil, runErr
	}
	var secrets list[tlsSecretItem]
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return nil, view.Errorf("kube.unreadable",
			"kubectl's answer for secrets could not be read: %v", err)
	}
	certs := make([]tlsCert, 0, len(secrets.Items))
	for _, sec := range secrets.Items {
		c := tlsCert{Namespace: sec.Metadata.Namespace, Name: sec.Metadata.Name}
		c.Subject, c.NotAfter, c.Problem = parseCert(sec.Data.Cert)
		certs = append(certs, c)
	}
	sort.Slice(certs, func(i, j int) bool {
		if certs[i].Namespace != certs[j].Namespace {
			return certs[i].Namespace < certs[j].Namespace
		}
		return certs[i].Name < certs[j].Name
	})
	return certs, nil
}

func runCertList(ctx context.Context, req plugin.Request) (view.View, error) {
	s, verr := selectionOf(req)
	if verr != nil {
		return nil, verr
	}
	read, secErr := tlsCerts(ctx, s, nil)
	if secErr != nil {
		return nil, secErr
	}

	cols := []view.Column{}
	if s.AllNS {
		cols = append(cols, view.Column{Name: "namespace"})
	}
	cols = append(cols,
		view.Column{Name: "secret"},
		view.Column{Name: "subject"},
		view.Column{Name: "not-after", Kind: view.KindTimestamp},
		view.Column{Name: "status", Kind: view.KindStatus},
	)
	now := time.Now()
	rows := make([][]string, 0, len(read.certs))
	for _, c := range read.certs {
		row := []string{}
		if s.AllNS {
			row = append(row, c.Namespace)
		}
		rows = append(rows, append(row, c.Name, c.Subject, c.expiry(), c.status(now)))
	}
	t := withEmpty(view.Table{Columns: cols, Rows: rows, Total: len(rows)},
		"no TLS Secrets in "+s.scopeWords())
	// Said once the read is old enough to matter: the overview's own tile
	// reads a moment before a listing of the same Secrets, and "read 0s ago"
	// is a note about nothing.
	if read.reused && time.Since(read.at) >= time.Minute {
		t.Warnings = append(t.Warnings, view.Error{
			Code:    "kube.cert.reused",
			Message: "read " + age(read.at) + " ago, not just now",
			Hint: "the TLS Secrets are read at most every " + format.Duration(certLife) +
				", because each read brings every private key in them across the wire",
		})
	}
	return t, nil
}

// warnDays mirrors builtin/internal/x509check.DefaultWarnDays — see the
// package comment above for why this plugin cannot import that constant
// instead of restating it.
const warnDays = 30

// parseCert decodes one Secret's tls.crt and keeps what is printed of it. A
// Secret that fails to decode or parse reports that as its problem rather than
// being dropped — silently skipping a certificate this capability exists to
// check is the one wrong way to fail here. Only the leaf certificate (the first
// PEM block, the order Kubernetes and every TLS peer both use) is judged; a
// bundle's intermediates typically outlive it by years and are not the
// question being asked.
func parseCert(b64PEM string) (subject string, notAfter time.Time, problem string) {
	raw, err := base64.StdEncoding.DecodeString(b64PEM)
	if err != nil {
		return "", time.Time{}, "tls.crt is not valid base64"
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", time.Time{}, "tls.crt has no parseable certificate"
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", time.Time{}, "tls.crt does not parse: " + err.Error()
	}
	return leaf.Subject.CommonName, leaf.NotAfter, ""
}

// status is the word a page prints for the certificate at the moment it is
// asked, which is not the moment it was read.
func (c tlsCert) status(now time.Time) string {
	switch {
	case c.Problem != "":
		return c.Problem
	case c.NotAfter.Before(now):
		return "expired"
	case c.NotAfter.Sub(now) < warnDays*24*time.Hour:
		return "expiring soon"
	}
	return "ok"
}

// expiry is the date a listing prints, empty for a certificate that did not
// parse.
func (c tlsCert) expiry() string {
	if c.NotAfter.IsZero() {
		return ""
	}
	return c.NotAfter.Format("2006-01-02")
}

// certPressure is kube.overview's own summary: which secrets are already
// past their certificate's expiry, and which are inside the warning window
// but not yet there. "secret/namespace" rather than the certificate's
// subject, because the subject is what the object is *for* and the secret
// name is what an operator runs `kubectl describe secret` against next.
func certPressure(certs []tlsCert, now time.Time) (expired, expiringSoon []string) {
	for _, c := range certs {
		label := c.Namespace + "/" + c.Name
		switch c.status(now) {
		case "expired":
			expired = append(expired, label)
		case "expiring soon":
			expiringSoon = append(expiringSoon, label)
		}
	}
	sort.Strings(expired)
	sort.Strings(expiringSoon)
	return expired, expiringSoon
}
