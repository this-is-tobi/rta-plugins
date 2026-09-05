# How the first-party rta plugins are built, checked and released.
#
# `make help` lists every target with its description. The listing is read out
# of this file, so it cannot drift from what is actually here.
#
# **Every directory under plugins/ holding a go.mod is a plugin**, and every
# plugin is a separate module pinned to a released rta in its own go.mod — it
# consumes the SDK exactly as a stranger's plugin would and cannot reach into
# rta's internals. There is no root module, so `go test ./...` from here
# compiles nothing; every target below walks the modules one at a time, and
# the list is read from the tree rather than kept by hand: a hand-maintained
# one once enumerated ten modules while eleven were in the tree, and the
# newest was the one nothing checked.
#
# Each folder its purpose: plugins/ holds the modules, index/ holds the
# manifests the release pipeline generated from their released binaries —
# what rta reads when this repository is attached with
# `rta plugin index add official`. Nothing under index/ is written by hand.

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Knobs
# ---------------------------------------------------------------------------

# Where `install` puts binaries: the same place `go install` uses, so a plugin
# lands beside the rta that has to find it on $PATH.
BINDIR ?= $(shell go env GOBIN)
ifeq ($(BINDIR),)
BINDIR := $(shell go env GOPATH)/bin
endif

# Local build output, and where `release` writes a plugin's archives.
BUILDDIR ?= bin
DISTDIR  ?= dist

# Where `index` writes a locally attachable index. Under dist/ because it is
# release output: its manifests state a checksum per artifact, so a committed
# copy would be claims that stopped being true at the next build.
INDEXDIR ?= $(DISTDIR)/index

# Where a manifest points a reader, and where a release's archives are served
# from. The download path is what GitHub emits for a tag containing slashes:
# the tag is percent-encoded once, `plugins%2Fpg%2Fv0.1.0`.
INDEX_HOMEPAGE ?= https://github.com/this-is-tobi/rta-plugins
RELEASE_URL    ?= https://github.com/this-is-tobi/rta-plugins/releases/download

# The platform whose binaries this machine can *run*. Reading a plugin's
# declaration means running it, so `index` describes this platform only and
# `index-release` extracts this platform's binary out of a release to read it.
HOST_OS       := $(shell go env GOOS)
HOST_ARCH     := $(shell go env GOARCH)
HOST_PLATFORM := $(HOST_OS)/$(HOST_ARCH)

# Every target a release ships. Six archives per plugin, built by `release`
# and compiled to /dev/null by `cross`.
RELEASE_TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64 windows/amd64 windows/arm64

# A git identity for the generated local index, which is a real repository
# because attaching one is a clone.
GIT_AS_RTA := -c user.email=rta@localhost -c user.name=rta -c commit.gpgsign=false

# The rta that `trust`, `index` and `index-release` call. `RTA=/path/to/rta`
# uses a specific build.
RTA ?= rta

# The rta checkout `dev` points the workspace at, and the ref `canary` clones.
RTA_DIR ?= ../rta
RTA_REF ?= main

# VERSION is what a build stamps into `var version` — empty means "read it
# off the plugin's own tag": `git describe --match 'plugins/<name>/v*'` less
# the prefix, or `dev` when no tag is in reach. `release` requires it
# explicitly, because a release is a fact somebody decided rather than one
# inferred from whatever tag happens to be nearest.
VERSION ?=

# $(call stamp,<name>) leaves the version to stamp in $$v.
stamp = v="$(VERSION)"; \
	if [ -z "$$v" ]; then v=$$(git describe --tags --match 'plugins/$(1)/v*' --dirty 2>/dev/null | sed 's|^plugins/$(1)/v||'); fi; \
	[ -n "$$v" ] || v=dev

# Release flags on the ordinary build: -s -w drop what nobody reads from a
# CLI binary, -trimpath keeps the build machine's layout out of the artifact.
GOBUILD = go build -trimpath -ldflags "-s -w -X main.version=$$v"

# Deterministic archives where the tools allow it. GNU tar (every Linux
# runner, so every release) takes an explicit member order, mtime and owner;
# bsdtar on macOS takes owner only, so a local `release` is a rehearsal whose
# bytes differ from CI's — the checksums still describe what was built.
TAR_IS_GNU := $(shell tar --version 2>/dev/null | grep -q GNU && echo yes)
ifeq ($(TAR_IS_GNU),yes)
TAR = tar --sort=name --mtime=@$$epoch --owner=0 --group=0 --numeric-owner
else
TAR = tar --uid 0 --gid 0 --numeric-owner
endif

SHA256 := $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo "shasum -a 256")

# Colours, unless the caller said not to.
ifdef NO_COLOR
CYAN  :=
BOLD  :=
RESET :=
else
CYAN  := \033[36m
BOLD  := \033[1m
RESET := \033[0m
endif

# ---------------------------------------------------------------------------
# The plugin modules, discovered rather than listed
# ---------------------------------------------------------------------------

PLUGINS := $(sort $(notdir $(patsubst %/go.mod,%,$(wildcard plugins/*/go.mod))))

# PLUGIN=<name> narrows every per-plugin target to one module. Validated, not
# filtered: a typo matching nothing would build nothing and exit 0.
ifdef PLUGIN
ifeq ($(filter $(PLUGIN),$(PLUGINS)),)
$(error no plugin named '$(PLUGIN)'. Have: $(PLUGINS))
endif
PLUGIN_LIST := $(PLUGIN)
else
PLUGIN_LIST := $(PLUGINS)
endif

# Static pattern rules over the module names, so the aggregate and the
# single-module form are one code path. Static rather than implicit on
# purpose: make skips implicit-rule search for a .PHONY target, and the
# ordinary `check-%:` form silently matched nothing.
CHECK_PLUGINS    := $(PLUGINS:%=check-%)
BUILD_PLUGINS    := $(PLUGINS:%=build-%)
INSTALL_PLUGINS  := $(PLUGINS:%=install-%)
TIDY_PLUGINS     := $(PLUGINS:%=tidy-%)
DOWNLOAD_PLUGINS := $(PLUGINS:%=download-%)

.PHONY: help setup tidy fmt fmt-check build install trust check cross \
	name-check replace-check docs-check docs docs-drift bump-rta index release index-release \
	dev dev-off canary ci list clean \
	$(CHECK_PLUGINS) $(BUILD_PLUGINS) $(INSTALL_PLUGINS) $(TIDY_PLUGINS) $(DOWNLOAD_PLUGINS)

##@ General

help: ## Print this help
	@awk 'BEGIN {FS = ":.*##"} \
		/^##@/ { printf "\n$(BOLD)%s$(RESET)\n", substr($$0, 5); next } \
		/^[a-zA-Z0-9_-]+:.*##/ { printf "  $(CYAN)%-16s$(RESET) %s\n", $$1, $$2 }' \
		$(MAKEFILE_LIST)
	@printf "\n$(BOLD)Notes$(RESET)\n"
	@printf "  Narrow any plugin target to one module:  $(CYAN)make check PLUGIN=pg$(RESET)\n"
	@printf "  Or address it directly:                  $(CYAN)make build-pg$(RESET)\n"
	@printf "  Installed here:                          %s\n" "$(BINDIR)"
	@printf "  Plugins in the tree:                     %s\n\n" "$(PLUGINS)"

##@ Setup

setup: $(PLUGIN_LIST:%=download-%) ## Fetch every module's dependencies — the one command after cloning
	@printf "\nReady. $(CYAN)make build$(RESET) for ./bin, $(CYAN)make help$(RESET) for everything else.\n"

$(DOWNLOAD_PLUGINS): download-%:
	@echo "==> plugins/$* (download)"
	@cd plugins/$* && go mod download

tidy: $(PLUGIN_LIST:%=tidy-%) ## Tidy every module's go.mod and go.sum

$(TIDY_PLUGINS): tidy-%:
	@echo "==> plugins/$* (tidy)"
	@cd plugins/$* && go mod tidy

fmt: ## Format every Go file
	@for p in $(PLUGIN_LIST); do gofmt -w plugins/$$p; done

fmt-check: ## Fail if anything is unformatted — `make fmt` fixes it
	@out=$$(for p in $(PLUGIN_LIST); do gofmt -l plugins/$$p; done); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out" | sed 's/^/  /'; exit 1; fi

##@ Build

build: $(PLUGIN_LIST:%=build-%) ## Build every plugin binary into ./bin

$(BUILD_PLUGINS): build-%: name-check
	@mkdir -p $(BUILDDIR)
	@$(call stamp,$*); echo "==> $(BUILDDIR)/rta-plugin-$* ($$v)"; \
	cd plugins/$* && $(GOBUILD) -o ../../$(BUILDDIR)/rta-plugin-$* .

install: $(PLUGIN_LIST:%=install-%) ## Install every plugin binary beside rta
ifeq ($(filter trust,$(MAKECMDGOALS)),)
	@echo
	@echo "Installed, and not yet allowed to run."
	@echo
	@echo "rta finds anything named rta-plugin-* on your PATH and loads it by"
	@echo "executing it, so discovery and approval are separate acts:"
	@echo
	@echo "    rta plugin trust           # what was found and not run"
	@echo "    rta plugin trust <name>    # approve that artifact"
	@echo "    make trust                 # approve the ones built here, and only those"
	@echo
endif

$(INSTALL_PLUGINS): install-%: name-check
	@mkdir -p "$(BINDIR)"
	@$(call stamp,$*); echo "==> $(BINDIR)/rta-plugin-$* ($$v)"; \
	cd plugins/$* && $(GOBUILD) -o "$(BINDIR)/rta-plugin-$*" .

# Only the modules in this tree, never what rta discovered on $PATH — so this
# cannot approve an rta-plugin-* that arrived from somewhere else. Nothing
# depends on this target and nothing acquires it as a side effect: the value
# of trust is that a person decided, and typing this is the decision.
trust: install ## Build, install and approve this repository's plugins
	@command -v $(RTA) >/dev/null || { echo "cannot run '$(RTA)': install rta, or pass RTA=/path/to/rta"; exit 1; }
	@for p in $(PLUGIN_LIST); do $(RTA) plugin trust $$p || exit 1; done

##@ Check

check: $(PLUGIN_LIST:%=check-%) ## Build, vet, test (race, shuffled) and format-check every module

$(CHECK_PLUGINS): check-%: name-check
	@echo "==> plugins/$*"
	@cd plugins/$* && go build -o /dev/null . && go vet ./... && go test ./... -count=1 -race -shuffle=on
	@test -z "$$(gofmt -l plugins/$*)" || { echo "gofmt needed in plugins/$*"; exit 1; }

cross: name-check ## Compile every plugin for every release target and discard the output
	@for p in $(PLUGIN_LIST); do for t in $(RELEASE_TARGETS); do \
		echo "==> plugins/$$p ($$t)"; \
		(cd plugins/$$p && CGO_ENABLED=0 GOOS=$${t%/*} GOARCH=$${t#*/} go build -o /dev/null .) || exit 1; \
	done; done

# A directory name is a plugin namespace and a word make splices into recipe
# lines. Make does not expand `$(...)` inside a value it read from the
# filesystem; it hands the characters to the shell, which performs the
# substitution — a directory named `$(touch${IFS}pwned)/` runs. So the names
# never reach a command line unchecked: `ls -1` is executed by the shell and
# its *output* is the names, which no later expansion touches. A prerequisite
# of every target that splices the list, so make stops before expanding.
name-check:
	@bad=$$(ls -1 plugins 2>/dev/null | grep -vE '^[a-z0-9][a-z0-9-]*$$' || true); \
	if [ -n "$$bad" ]; then \
		echo "plugins/ entry is not a plugin namespace:"; echo "$$bad" | sed 's/^/  /'; \
		echo "lowercase letters, digits and dashes"; exit 1; \
	fi

# Any replace at all, not only an absolute one: every go.mod here pins a
# released rta, and a replace makes that pin a fiction that builds on one
# machine. `make dev` is the edit loop, and it writes go.work, never go.mod.
replace-check: name-check
	@bad=$$(grep -l '^replace ' plugins/*/go.mod 2>/dev/null || true); \
	if [ -n "$$bad" ]; then \
		echo "a replace directive — the pin in go.mod is what CI and releases build against:"; \
		echo "$$bad" | sed 's/^/  /'; echo "use 'make dev RTA_DIR=<rta checkout>' for an edit loop"; exit 1; \
	fi

# Two things the source has to say about itself.
#
# README's table states how many capabilities each plugin carries, so a
# reader knows what they are opening. Counted from the source: an `ID:
# "pg.dump"` literal is what a capability ID is and what nothing else in
# these files looks like. Seven of eleven counts were stale at once in rta's
# author guide before this check existed there.
#
# And a whole-store backup — `<plugin>.dump` or `<plugin>.snapshot` — has to
# tell the person taking it what it does not carry: a dump that restores only
# with material living outside it is not yet a backup, and the receipt is the
# one moment somebody is looking. rta's recipes chapter plans for the same
# set from its side, by a list kept by hand there; this is the half that can
# read the declaration.
# A plugin's README.md is its declaration rendered, never prose somebody
# keeps in step by hand: `rta plugin doc` runs the binary the way a load does
# and writes the page from what it declares, so the page cannot disagree with
# the plugin beside it. docs-drift is the gate that keeps the committed copy
# honest. It steps aside, loudly, on an rta too old to have the command — the
# alternative is a CI that fails on every machine until rta ships it.
docs: build ## Regenerate every plugin's README.md from its binary's declaration
	@command -v $(RTA) >/dev/null || { echo "cannot run '$(RTA)': install rta, or pass RTA=/path/to/rta"; exit 1; }
	@for p in $(PLUGIN_LIST); do \
		$(RTA) plugin doc $(BUILDDIR)/rta-plugin-$$p > plugins/$$p/README.md || exit 1; \
		echo "==> plugins/$$p/README.md"; \
	done

docs-drift: build ## Fail if a plugin's README.md is behind its binary's declaration
	@if ! $(RTA) plugin --help 2>/dev/null | grep -qE '^ +doc '; then \
		echo "docs-drift: '$(RTA) plugin doc' is unavailable, README drift not checked"; exit 0; fi; \
	fail=0; for p in $(PLUGIN_LIST); do \
		$(RTA) plugin doc $(BUILDDIR)/rta-plugin-$$p | diff -q - plugins/$$p/README.md >/dev/null \
			|| { echo "plugins/$$p/README.md is behind its declaration; run 'make docs'"; fail=1; }; \
	done; exit $$fail

docs-check: name-check ## Fail if README's counts or a backup's receipt disagree with the declarations
	@fail=0; for p in $(PLUGINS); do \
		src=$$(ls plugins/$$p/*.go | grep -v '_test\.go$$'); \
		want=$$(echo "$$src" | xargs grep -ohE '\bID:[[:space:]]*"[a-z0-9]+(\.[a-z0-9]+)+"' | sort -u | wc -l | tr -d ' '); \
		got=$$(grep -E "^\| \[\`$$p\`\]" README.md | awk -F'|' '{gsub(/ /,"",$$4); print $$4}'); \
		if [ -z "$$got" ]; then echo "README.md never lists $$p"; fail=1; \
		elif [ "$$got" != "$$want" ]; then echo "README.md says $$p has $$got capabilities; it declares $$want"; fail=1; fi; \
		for id in $$(echo "$$src" | xargs grep -ohE "\bID:[[:space:]]*\"$$p\.(dump|snapshot)\"" | sed -E 's/.*"([^"]+)"/\1/' | sort -u); do \
			echo "$$src" | xargs grep -q '"does not carry"' || { echo "$$id backs up a whole datastore and its receipt has no 'does not carry' row"; fail=1; }; \
		done; \
	done; exit $$fail

##@ Index

# Every claim in a manifest is checked against the artifact at somebody
# else's install, so none is written by hand: `rta plugin manifest` runs the
# binary the way a load does and writes down what it declares. What comes
# out here is attachable — a real repository whose artifact URLs point at the
# binaries just built — so installing from it performs the same fetch, hash
# and sandboxed declaration check a published index gets.
index: build ## Generate an attachable index from the binaries just built, for this platform
	@command -v $(RTA) >/dev/null || { echo "cannot run '$(RTA)': install rta, or pass RTA=/path/to/rta"; exit 1; }
	@rm -rf $(INDEXDIR); mkdir -p $(INDEXDIR)/index
	@for p in $(PLUGIN_LIST); do \
		$(RTA) plugin manifest $(BUILDDIR)/rta-plugin-$$p --index $(INDEXDIR) \
			--homepage $(INDEX_HOMEPAGE)/tree/main/plugins/$$p \
			--platform $(HOST_PLATFORM)=$(BUILDDIR)/rta-plugin-$$p >/dev/null || exit 1; \
		echo "==> $(INDEXDIR)/index/$$p.yaml"; \
	done
	@git -C $(INDEXDIR) init --quiet
	@git -C $(INDEXDIR) $(GIT_AS_RTA) add .
	@git -C $(INDEXDIR) $(GIT_AS_RTA) commit --quiet -m "local ($(HOST_PLATFORM))"
	@printf "\n$(BOLD)%s$(RESET) holds %s manifests for %s. Attach it:\n\n    rta plugin index add local %s\n\n" \
		"$(INDEXDIR)" "$(words $(PLUGIN_LIST))" "$(HOST_PLATFORM)" "$$(cd $(INDEXDIR) && pwd)"

##@ Release

# One plugin, one version, six archives — what CD runs per released plugin.
#
# The archive holds the binary at its root under the name rta discovers it
# by (`rta-plugin-<name>`, `.exe` on Windows), plus LICENSE and NOTICE. A
# .tar.gz and never a zip: rta extracts one member from a gzipped tar and
# has no zip reader, so a zip is an artifact `rta plugin install` cannot
# open, on the one platform nobody developing on Linux or macOS would notice.
release: name-check ## Build every release archive for PLUGIN at VERSION into dist/<PLUGIN>
	@test -n "$(PLUGIN)" || { echo "release needs PLUGIN=<name>"; exit 1; }
	@test -n "$(VERSION)" || { echo "release needs VERSION=<x.y.z>"; exit 1; }
	@rm -rf $(DISTDIR)/$(PLUGIN); mkdir -p $(DISTDIR)/$(PLUGIN); out=$$(cd $(DISTDIR)/$(PLUGIN) && pwd); \
	epoch=$$(git log -1 --format=%ct 2>/dev/null || date +%s); v="$(VERSION)"; \
	for t in $(RELEASE_TARGETS); do \
		os=$${t%/*}; arch=$${t#*/}; bin=rta-plugin-$(PLUGIN); \
		[ "$$os" = windows ] && bin=$$bin.exe; \
		stage=$$(mktemp -d); \
		(cd plugins/$(PLUGIN) && CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GOBUILD) -o "$$stage/$$bin" .) || exit 1; \
		cp LICENSE NOTICE "$$stage/"; \
		archive="$$out/rta-plugin-$(PLUGIN)_$${v}_$${os}_$${arch}.tar.gz"; \
		(cd "$$stage" && $(TAR) -cf - $$bin LICENSE NOTICE | gzip -n > "$$archive") || exit 1; \
		rm -rf "$$stage"; echo "==> $$archive"; \
	done; \
	(cd "$$out" && $(SHA256) *.tar.gz > checksums.txt) && echo "==> $$out/checksums.txt"

# The other half of a release: the manifest, generated from the archives
# `release` built and the URLs the release serves them at. The binary is run
# to read its declaration — this platform's, extracted from its own archive
# — and every other platform is described by the checksums file.
index-release: name-check ## Regenerate index/<name>.yaml for PLUGINS_RELEASED from dist/<name>
	@test -n "$(PLUGINS_RELEASED)" || { echo "index-release needs PLUGINS_RELEASED=\"pg kube\""; exit 1; }
	@command -v $(RTA) >/dev/null || { echo "cannot run '$(RTA)': install rta, or pass RTA=/path/to/rta"; exit 1; }
	@for p in $(PLUGINS_RELEASED); do \
		d=$(DISTDIR)/$$p; \
		host=$$(ls $$d/rta-plugin-$${p}_*_$(HOST_OS)_$(HOST_ARCH).tar.gz 2>/dev/null | head -1); \
		[ -n "$$host" ] || { echo "$$d holds no $(HOST_PLATFORM) archive to read a declaration from"; exit 1; }; \
		version=$$(basename "$$host" | sed -E "s|^rta-plugin-$${p}_(.*)_$(HOST_OS)_$(HOST_ARCH)\.tar\.gz$$|\1|"); \
		stage=$$(mktemp -d); tar -xzf "$$host" -C "$$stage" rta-plugin-$$p || exit 1; \
		flags=""; for t in $(RELEASE_TARGETS); do \
			flags="$$flags --platform $$t=$(RELEASE_URL)/plugins%2F$${p}%2Fv$${version}/rta-plugin-$${p}_$${version}_$${t%/*}_$${t#*/}.tar.gz"; \
		done; \
		$(RTA) plugin manifest "$$stage/rta-plugin-$$p" --checksums "$$d/checksums.txt" \
			--homepage $(INDEX_HOMEPAGE)/tree/main/plugins/$$p --index . $$flags >/dev/null || exit 1; \
		rm -rf "$$stage"; echo "==> index/$$p.yaml ($$version)"; \
	done

##@ SDK development

# The edit loop against an unreleased rta. go.work is never committed (see
# .gitignore): every go.mod keeps pinning a released rta, which is what CI
# and releases build, while this machine builds against the checkout.
dev: ## Point a workspace at RTA_DIR (default ../rta) for an SDK edit loop
	@test -f "$(RTA_DIR)/go.mod" || { echo "$(RTA_DIR) is not an rta checkout — RTA_DIR=<path>"; exit 1; }
	@rm -f go.work go.work.sum
	@go work init $(PLUGINS:%=./plugins/%)
	@go work edit -replace github.com/this-is-tobi/rta=$(RTA_DIR)
	@echo "go.work points github.com/this-is-tobi/rta at $(RTA_DIR). 'make dev-off' removes it."

dev-off: ## Remove the workspace; builds pin go.mod's rta again
	@rm -f go.work go.work.sum && echo "go.work removed"

# Does the SDK still fit these plugins? go.mod's pin means a breaking change
# in rta breaks nothing here until somebody bumps — the right v0 semantics
# and the wrong signal for a project re-architecting before v1. This builds
# and tests every plugin against rta at RTA_REF instead, and a red run says
# "the SDK moved under you" without blocking a release.
# The SDK bump, in one move: every go.mod, every go.sum, and .rta-version —
# the pin the workflows `go install` the manifest generator (cd.yml) and the
# README generator (ci.yml) from. Those are pinned on purpose (the rta that
# renders a claim decides what the claim says), and the pin lives in a plain
# file rather than in the workflow lines so that moving it never touches
# .github/workflows/ — a path an App token may not write without a
# permission that would apply to every repository the App is on. bump-rta.yml
# runs this on a schedule; running it by hand is the same thing sooner.
bump-rta: name-check ## Pin every module and .rta-version to rta RTA_VERSION (e.g. RTA_VERSION=v0.9.0)
	@test -n "$(RTA_VERSION)" || { echo "bump-rta needs RTA_VERSION=vX.Y.Z"; exit 1; }
	@for p in $(PLUGIN_LIST); do \
		echo "==> plugins/$$p ($(RTA_VERSION))"; \
		(cd plugins/$$p && go get github.com/this-is-tobi/rta@$(RTA_VERSION) >/dev/null 2>&1 && go mod tidy) || exit 1; \
	done
	@printf '%s\n' "$(RTA_VERSION)" > .rta-version && echo "==> .rta-version ($(RTA_VERSION))"

canary: name-check ## Check every plugin against rta at RTA_REF (default main), without touching go.mod
	@tmp=$$(mktemp -d); \
	git clone --quiet --depth 1 --branch $(RTA_REF) https://github.com/this-is-tobi/rta "$$tmp/rta" || exit 1; \
	$(MAKE) dev RTA_DIR="$$tmp/rta" && $(MAKE) check; rc=$$?; \
	$(MAKE) dev-off; rm -rf "$$tmp"; exit $$rc

##@ Everything

ci: fmt-check name-check replace-check docs-check check docs-drift cross ## Everything CI runs
	@printf "\nci: green — every module built, vetted, tested and cross-compiled.\n\n"

##@ Housekeeping

list: name-check ## Print the plugin names, one per line
	@printf '%s\n' $(PLUGINS)

clean: ## Remove build output
	rm -rf $(BUILDDIR) $(DISTDIR)
