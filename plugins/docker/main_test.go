package main

import (
	"testing"

	"github.com/this-is-tobi/rta/pkg/sdk/sdktest"
)

// sdktest is the definition of "a correct plugin" — no exemption for docker,
// and until now no test file at all, which is the same exemption taken
// silently. Three mutating capabilities (stop, restart, rm) had nothing
// checking that their dry runs describe rather than act, and rm is
// Destructive: `--dry-run` is an accepted substitute for `--yes` on a
// Destructive capability, so the dry-run rule is the only thing standing
// between "preview" and an unconfirmed removal.
//
// conformanceInputs aims the five container capabilities at sdktest.Nowhere,
// with a container that does not exist, so the operator's own daemon is never
// asked and a dry run that connects is seen doing it. Each of these does, on
// purpose: stop, restart and rm find the container before they say what they
// would do to it, and inspect and env are classified write for what they
// disclose, so their dry run is the read. Each is waived from
// RuleDryRunOffline for that, and still held to RuleDryRun's data directory.
func TestConformance(t *testing.T) {
	sdktest.Check(t, Plugin(), sdktest.WithInputs(conformanceInputs), sdktest.WithSource("."),
		sdktest.Skip(sdktest.RuleDryRunOffline, "docker.container.stop", "finds the container a stop would reach"),
		sdktest.Skip(sdktest.RuleDryRunOffline, "docker.container.restart", "finds the container a restart would reach"),
		sdktest.Skip(sdktest.RuleDryRunOffline, "docker.container.rm", "finds the stopped container a removal would reach"),
		sdktest.Skip(sdktest.RuleDryRunOffline, "docker.container.inspect", disclosed),
		sdktest.Skip(sdktest.RuleDryRunOffline, "docker.container.env", disclosed))
}

// disclosed is why a capability classified write for what it discloses
// connects under --dry-run.
const disclosed = "classified write for what it discloses, so its dry run is the read"

func conformanceInputs(dir string) map[string]map[string]any {
	container := map[string]any{"container": "rta-conformance-does-not-exist", "host": "tcp://" + sdktest.Nowhere(dir)}
	return map[string]map[string]any{
		"docker.container.stop":    container,
		"docker.container.restart": container,
		"docker.container.rm":      container,
		"docker.container.inspect": container,
		"docker.container.env":     container,
	}
}
