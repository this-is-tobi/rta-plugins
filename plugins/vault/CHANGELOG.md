# Changelog

## [0.4.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.15...plugins/vault/v0.4.16) (2026-09-28)


### Bug Fixes

* **vault:** a ca-file that cannot be used names ca-file as its reader sets it ([8021f3f](https://github.com/this-is-tobi/rta-plugins/commit/8021f3fe959141c9aa8ea658663096634da75cd6))
* **vault:** a name DNS cannot resolve is named, with the lookup that shows what DNS returns ([4031ec6](https://github.com/this-is-tobi/rta-plugins/commit/4031ec6bece47261234f662c46cdc613b1125d48))
* **vault:** an untrusted certificate names ca-file as the operator's setting, never as passed ([e88eb3b](https://github.com/this-is-tobi/rta-plugins/commit/e88eb3b9b1417bc81800e369e56b54264a58a933))
* **vault:** the snapshot receipt names a restore into the Vault the snapshot came from ([a5116c8](https://github.com/this-is-tobi/rta-plugins/commit/a5116c8dba55d95bc8d281386b6f373411e98bc6))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.4.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.14...plugins/vault/v0.4.15) (2026-09-28)


### Bug Fixes

* **vault:** a message names capabilities and inputs the way its reader's surface gives them ([b97c52d](https://github.com/this-is-tobi/rta-plugins/commit/b97c52dc862be543ab44e19547bc4d4174377a46))
* **vault:** a name DNS cannot resolve is reported as that, not as nothing listening ([5076a31](https://github.com/this-is-tobi/rta-plugins/commit/5076a313b1f13e91e1936e7cbea0ba6ebefc191b))
* **vault:** a refused request points at vault.token.status, a capability rta has ([a896dd3](https://github.com/this-is-tobi/rta-plugins/commit/a896dd3428bbc888bdd786bffa032bdd01a4c562))
* **vault:** a truncated tree points at the path argument, which the CLI takes by its place ([5e3f418](https://github.com/this-is-tobi/rta-plugins/commit/5e3f418f5bbb4e4fad6493aa5d9dddaa5946c71d))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.4.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.13...plugins/vault/v0.4.14) (2026-09-27)


### Bug Fixes

* **vault:** a kv set or wrap set preview of one field counts it as "1 field" ([89c2d45](https://github.com/this-is-tobi/rta-plugins/commit/89c2d45abdbce0df57b5020420e1a0414ab122f4))
* **vault:** a tree holding one secret or one folder counts it in the singular ([4478fe0](https://github.com/this-is-tobi/rta-plugins/commit/4478fe0fe281d066699ebaf0288c3a510af04db1))


### Code Refactoring

* **vault:** each byte count reaches format.Bytes as the integer it arrives as ([0423e3c](https://github.com/this-is-tobi/rta-plugins/commit/0423e3c3884c85f4ede672accfbb1e229f893a9e))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.4.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.12...plugins/vault/v0.4.13) (2026-09-26)


### Code Refactoring

* **vault:** say that unknownMount's silence is deliberate ([0683a19](https://github.com/this-is-tobi/rta-plugins/commit/0683a197c33f99dc5d8a7721b83f25b778eca7d8))
* **vault:** take the tilde rule from the SDK rather than keeping a copy ([2249703](https://github.com/this-is-tobi/rta-plugins/commit/22497032970e84c1848e5378a1914d066ae4d869))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.4.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.11...plugins/vault/v0.4.12) (2026-09-22)


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.4.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.10...plugins/vault/v0.4.11) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.4.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.9...plugins/vault/v0.4.10) (2026-09-20)


### Bug Fixes

* **vault:** overview says which read failed, instead of dropping its row ([3121493](https://github.com/this-is-tobi/rta-plugins/commit/312149339e84b57415ec04d38153d0a0fad9f9cd))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.4.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.8...plugins/vault/v0.4.9) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.4.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.7...plugins/vault/v0.4.8) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.4.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.6...plugins/vault/v0.4.7) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.4.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.5...plugins/vault/v0.4.6) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.4.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.4...plugins/vault/v0.4.5) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.4.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.3...plugins/vault/v0.4.4) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.4.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.2...plugins/vault/v0.4.3) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.4.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.1...plugins/vault/v0.4.2) (2026-09-08)


### Bug Fixes

* **vault:** bind the mount on kv and transit capabilities ([16555c6](https://github.com/this-is-tobi/rta-plugins/commit/16555c69839d72495140c1fa4b995cab6830d3d0))


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.4.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.4.0...plugins/vault/v0.4.1) (2026-09-07)


### Bug Fixes

* **vault:** a mount that does not exist is named, not shown as an empty one ([7331134](https://github.com/this-is-tobi/rta-plugins/commit/7331134b1d4c3d80acf913dedbdcaab7ae8e3abc))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.3.2...plugins/vault/v0.4.0) (2026-09-06)


### Features

* **vault:** a secret's versions can be listed, read back, deleted, undeleted and destroyed ([784f19a](https://github.com/this-is-tobi/rta-plugins/commit/784f19a3c7d5e2285602c7c3be2669d579b982b7))


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.3.1...plugins/vault/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.3.0...plugins/vault/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.2.1...plugins/vault/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.2.0...plugins/vault/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/vault/v0.1.0...plugins/vault/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
