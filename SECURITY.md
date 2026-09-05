# Security policy

These plugins run inside rta's sandbox and are what an AI agent reaches your databases, clusters and buckets through — a vulnerability in one is not an ordinary bug. Please report it privately rather than opening a public issue.

## Reporting a vulnerability

Email **this-is-tobi@proton.me** with a description and, if you have one, a reproduction. You should get an acknowledgement within a few days.

Please don't open a public GitHub issue for a security report until a fix is available.

## Supported versions

Every plugin here is pre-1.0 and versioned on its own. Only the latest release of each is supported; there is no backport policy yet.

## Scope

In scope: the eleven plugin binaries this repository releases, the manifests under `index/` (the index rta installs them from), and the release pipeline that produces both. rta's own verification of a plugin — digest checking, declaration checking, confinement — is [rta](https://github.com/this-is-tobi/rta)'s scope; report those there.
