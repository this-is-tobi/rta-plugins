# Changelog

## [0.3.18](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.17...plugins/mysql/v0.3.18) (2026-09-28)


### Bug Fixes

* **mysql:** the dump receipt's restore line keeps the TLS the dump was taken over ([49bb8fa](https://github.com/this-is-tobi/rta-plugins/commit/49bb8fa3dd71c8029554561019033d8b6381e26e))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.3.17](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.16...plugins/mysql/v0.3.17) (2026-09-28)


### Bug Fixes

* **mysql:** a flag of mysqldump, the client or the server is named with its program, never bare ([0d068b2](https://github.com/this-is-tobi/rta-plugins/commit/0d068b236e354d0213ebff09c35aa33c9595bcd9))
* **mysql:** a message names capabilities and inputs the way its reader's surface gives them ([e926105](https://github.com/this-is-tobi/rta-plugins/commit/e9261054cf03793f4dbc9ee5f446951ba1270291))
* **mysql:** a name DNS cannot resolve is reported as that, not as nothing listening ([4ff50c9](https://github.com/this-is-tobi/rta-plugins/commit/4ff50c96d7f96bb9142b5bc94b6110b60e0b3df2))
* **mysql:** the query's description says it needs a grant, as every write now does ([19d4099](https://github.com/this-is-tobi/rta-plugins/commit/19d4099c3c583840554fdcfa9046290255991d2d))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.3.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.15...plugins/mysql/v0.3.16) (2026-09-27)


### Bug Fixes

* **mysql:** a dump receipt counts one table read live as "1 non-transactional table was" ([dfa3400](https://github.com/this-is-tobi/rta-plugins/commit/dfa3400d5914fdf0076668d91c4a59e55cd998d9))
* **mysql:** a limit that leaves one table or row out counts it in the singular ([5329c68](https://github.com/this-is-tobi/rta-plugins/commit/5329c68b7ce3388c90db509a4cbc7f7cfc724963))


### Code Refactoring

* **mysql:** each byte count reaches format.Bytes as the integer it arrives as ([1b8fdb0](https://github.com/this-is-tobi/rta-plugins/commit/1b8fdb0503fa35da82fe593be85bf1bd4a6bfa75))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.3.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.14...plugins/mysql/v0.3.15) (2026-09-26)


### Code Refactoring

* **mysql:** take the tilde rule from the SDK rather than keeping a copy ([f49a619](https://github.com/this-is-tobi/rta-plugins/commit/f49a6199a04ec3f6b3d0e928de3bd4a94c9b8174))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.3.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.13...plugins/mysql/v0.3.14) (2026-09-22)


### Bug Fixes

* **mysql:** a one-table schema said "1 tables" ([a49e505](https://github.com/this-is-tobi/rta-plugins/commit/a49e50505f030c1700904cb165c9d3e9029001b2))


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.3.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.12...plugins/mysql/v0.3.13) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.3.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.11...plugins/mysql/v0.3.12) (2026-09-20)


### Bug Fixes

* **mysql:** what the grants hide is said, instead of passing for the whole ([fd9046f](https://github.com/this-is-tobi/rta-plugins/commit/fd9046f6ab151b7b46e5a9193c8725c3f303498e))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.3.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.10...plugins/mysql/v0.3.11) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.3.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.9...plugins/mysql/v0.3.10) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.3.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.8...plugins/mysql/v0.3.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.3.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.7...plugins/mysql/v0.3.8) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.3.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.6...plugins/mysql/v0.3.7) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.3.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.5...plugins/mysql/v0.3.6) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.3.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.4...plugins/mysql/v0.3.5) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.3.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.3...plugins/mysql/v0.3.4) (2026-09-08)


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.3.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.2...plugins/mysql/v0.3.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.1...plugins/mysql/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.0...plugins/mysql/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.2.1...plugins/mysql/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.2.0...plugins/mysql/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.1.0...plugins/mysql/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
