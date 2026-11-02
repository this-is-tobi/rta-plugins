// vault is an external plugin — its own module, consuming the SDK exactly as
// a stranger would, the same reasoning pg documents.
//
module github.com/this-is-tobi/rta-plugins/plugins/vault

go 1.27.3

// Keeps macOS's own certificate verifier when SSL_CERT_FILE or SSL_CERT_DIR
// is set, as rta's go.mod does, and says why. Go 1.31 removes the setting
// and refuses this line; the check recipe in the Makefile says what then.
godebug x509sslcertoverrideplatform=0

require (
	github.com/hashicorp/vault/api v1.23.0
	github.com/this-is-tobi/rta v0.57.0
)

require (
	charm.land/bubbles/v2 v2.2.1 // indirect
	charm.land/bubbletea/v2 v2.0.8 // indirect
	charm.land/huh/v2 v2.0.3 // indirect
	charm.land/lipgloss/v2 v2.0.6 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/catppuccin/go v0.2.0 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260811164956-006e29f97886 // indirect
	github.com/charmbracelet/x/ansi v0.11.8 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/exp/strings v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/go-jose/go-jose/v4 v4.1.6 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/guptarohit/asciigraph v0.10.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.2.0 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.2 // indirect
	github.com/hashicorp/go-sockaddr v1.0.7 // indirect
	github.com/hashicorp/hcl v1.0.1-vault-7 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.27 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/hashstructure/v2 v2.0.2 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260818201246-1b0934165a6f // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
