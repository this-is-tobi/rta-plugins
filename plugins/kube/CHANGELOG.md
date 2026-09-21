# Changelog

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.12...plugins/kube/v0.4.0) (2026-09-21)


### Features

* **kube:** a kube.overview tile re-runs every minute, not every few seconds ([778f998](https://github.com/this-is-tobi/rta-plugins/commit/778f9982ecdbc7f6a467aa01eedc382f48d5b1fb))


### Bug Fixes

* **kube:** a credential plugin that is not installed is said so, not reported as an expired one ([edbcf01](https://github.com/this-is-tobi/rta-plugins/commit/edbcf01899419227b4d2d818359c802eeac36923))
* **kube:** a credential plugin that refused is a sign-in problem, named with its plugin ([b89d165](https://github.com/this-is-tobi/rta-plugins/commit/b89d165bc890849a2c7c44a2adf286b6db5c2c5f))


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.3.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.11...plugins/kube/v0.3.12) (2026-09-20)


### Bug Fixes

* **kube:** a bare ~ in --out is the home directory, not a file named "~" ([e5e7e2e](https://github.com/this-is-tobi/rta-plugins/commit/e5e7e2e17fef9cce4902998bfb0fefe502941aa7))
* **kube:** a token file that could not be closed is reported, not called written ([5313efa](https://github.com/this-is-tobi/rta-plugins/commit/5313efa43c9859258dd27a9c516bfc2e95e45384))
* **kube:** three reads that could not answer said nothing about it ([27ddd46](https://github.com/this-is-tobi/rta-plugins/commit/27ddd46ca3eb25f65fbf9a7c93f6999ee3c93c8b))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.3.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.10...plugins/kube/v0.3.11) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.3.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.9...plugins/kube/v0.3.10) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.3.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.8...plugins/kube/v0.3.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.3.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.7...plugins/kube/v0.3.8) (2026-09-14)


### Bug Fixes

* **kube:** parse the nanocore cpu usage metrics-server reports ([16f62c9](https://github.com/this-is-tobi/rta-plugins/commit/16f62c968ec44acc6300f9247f9893fced6b30aa))


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.3.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.6...plugins/kube/v0.3.7) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.3.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.5...plugins/kube/v0.3.6) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.3.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.4...plugins/kube/v0.3.5) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.3.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.3...plugins/kube/v0.3.4) (2026-09-08)


### Bug Fixes

* **kube:** bind the namespace on serviceaccount.revoke ([cc78fe1](https://github.com/this-is-tobi/rta-plugins/commit/cc78fe102d72c1814d87bfc1c55638abebd4c903))
* **kube:** validate the namespace before it reaches a raw API path ([e468da1](https://github.com/this-is-tobi/rta-plugins/commit/e468da128fc213c2f173525a8afe8714894fc57b))


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.3.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.2...plugins/kube/v0.3.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.1...plugins/kube/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.3.0...plugins/kube/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.2.1...plugins/kube/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.2.0...plugins/kube/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/kube/v0.1.0...plugins/kube/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))
* the scope a call works in is a flag a profile can carry ([763fe58](https://github.com/this-is-tobi/rta-plugins/commit/763fe58bdcacfd3a677e491af99c8c42ef2a5d3f))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
