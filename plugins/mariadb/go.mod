// mariadb is a separate module, like every plugin here: a first-party plugin is
// the proof the SDK works, and it only proves it by consuming the SDK exactly
// as a stranger would. A stranger cannot reach into rta's internal packages,
// and neither can this.
//
module github.com/this-is-tobi/rta-plugins/plugins/mariadb

go 1.27.3

// Keeps macOS's own certificate verifier when SSL_CERT_FILE or SSL_CERT_DIR
// is set, as rta's go.mod does, and says why. Go 1.31 removes the setting
// and refuses this line; the check recipe in the Makefile says what then.
godebug x509sslcertoverrideplatform=0

require (
	github.com/this-is-tobi/rta v0.57.0
	github.com/this-is-tobi/rta-plugins/plugins/mysql v0.13.1
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/go-sql-driver/mysql v1.10.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/oklog/run v1.1.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260818201246-1b0934165a6f // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
