# Changelog

## [0.5.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.4.3...plugins/s3/v0.5.0) (2026-10-03)


### Features

* **s3:** tls-server-name checks a forwarded server's certificate for its own name ([0e4099e](https://github.com/this-is-tobi/rta-plugins/commit/0e4099e7e17a6429cbc38c5bbf59a472559c80b6))


### Bug Fixes

* **s3:** a certificate is read before the dial, whatever names the certificate holds ([48afa8d](https://github.com/this-is-tobi/rta-plugins/commit/48afa8ddc6ff45504e7e5b16f405f19de1583c26))
* **s3:** a connection failure is read by rta's SDK, a revoked certificate never as untrusted ([5d80ec0](https://github.com/this-is-tobi/rta-plugins/commit/5d80ec066e0a28f5ac094c5347212390cd354cf0))
* **s3:** ca-file resolves a leading ~, as the other paths do ([dbe3fa0](https://github.com/this-is-tobi/rta-plugins/commit/dbe3fa0cc660c12daacc9147c248dbfc97c23209))
* **s3:** plain HTTP to a server that speaks only HTTPS is named as the scheme to change ([c448ae2](https://github.com/this-is-tobi/rta-plugins/commit/c448ae26ae0fb9909c1ce9f1f7367cbe2878743c))
* **s3:** the removal a taken destination offers reaches the server the copy reached ([bea142e](https://github.com/this-is-tobi/rta-plugins/commit/bea142ef1df6d4993ef2332bd59a580f461f82db))


### Code Refactoring

* **s3:** hints name settings, the explain page and a DNS lookup in the SDK's words ([38a5de6](https://github.com/this-is-tobi/rta-plugins/commit/38a5de64e160503123968c7e2c48b8a2e9c9b06e))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.4.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.4.2...plugins/s3/v0.4.3) (2026-09-29)


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.4.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.4.1...plugins/s3/v0.4.2) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

## [0.4.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.4.0...plugins/s3/v0.4.1) (2026-09-28)


### Bug Fixes

* **s3:** a ca-file that cannot be used says what it must hold, named as its reader sets it ([620a17e](https://github.com/this-is-tobi/rta-plugins/commit/620a17ecb0a1348e6f2242b3d05f63d5549fac31))
* **s3:** a name DNS cannot resolve is named alone, without the port beside it ([a507954](https://github.com/this-is-tobi/rta-plugins/commit/a5079540866abc3c8f089ac81a945a8965726b5e))
* **s3:** an untrusted certificate is answered with the CA to trust, never with TLS off ([d5de116](https://github.com/this-is-tobi/rta-plugins/commit/d5de116d03e39396f2847e74befe0764c96e9bb8))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.16...plugins/s3/v0.4.0) (2026-09-28)


### ⚠ BREAKING CHANGES

* **s3:** s3.object.set with --file naming standard input, as /dev/stdin or as descriptor 0 under /dev/fd or /proc, is refused as s3.file.stdin, where it stored an empty object.
* **s3:** a copy or rename grant naming only the source key, or a prefix holding it, no longer authorizes writing the object under a key the grant does not cover.

### Bug Fixes

* **s3:** a copy or rename grant has to cover the key it writes as well as the one it reads ([b448fa8](https://github.com/this-is-tobi/rta-plugins/commit/b448fa83b427ac0e7c5a41f9289966a19b1b20fc))
* **s3:** a message names capabilities and inputs the way its reader's surface gives them ([fce1b7b](https://github.com/this-is-tobi/rta-plugins/commit/fce1b7bd83bc8eb7309535e1b78712ae7d263344))
* **s3:** a missing object points at the listing with its bucket as the flag the CLI takes ([130eaa4](https://github.com/this-is-tobi/rta-plugins/commit/130eaa4775d116bfc57603333fd209f3886ccb84))
* **s3:** a name DNS cannot resolve is reported as that, not as nothing listening ([b6b41f2](https://github.com/this-is-tobi/rta-plugins/commit/b6b41f28d0152f85cfa5a826a0587ed024e90ad6))
* **s3:** copy, rename and set name their inputs without a flag, and s3 carries the spelling guard ([c41f7e2](https://github.com/this-is-tobi/rta-plugins/commit/c41f7e2d61356e138362d02a8548464f3980999c))
* **s3:** object set --dry-run says a stream's size is unknown rather than 0 B ([2b886ab](https://github.com/this-is-tobi/rta-plugins/commit/2b886aba4c0048c1e1a00375d6fbf4c5b393870c))
* **s3:** object set --file streams a pipe or a device rather than failing to seek it ([e52dbd8](https://github.com/this-is-tobi/rta-plugins/commit/e52dbd8300935ce263c968ba07927a861cf8485f))
* **s3:** object set refuses --file naming standard input rather than storing an empty object ([7c15e1e](https://github.com/this-is-tobi/rta-plugins/commit/7c15e1e1d9bc9c82515004d76490b5b962cc4f36))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.3.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.15...plugins/s3/v0.3.16) (2026-09-27)


### Bug Fixes

* **s3:** a transfer refused at one file, object or symlink counts it in the singular ([1e63adf](https://github.com/this-is-tobi/rta-plugins/commit/1e63adfb8f369f892d9320fe0af2d5a4cc49f2e6))
* **s3:** a tree over one object counts it as "1 object" ([d814043](https://github.com/this-is-tobi/rta-plugins/commit/d81404310ace3143713afffd579e37882c2f9323))
* **s3:** an object that is not text is dumped rather than printed as stray letters ([caebb55](https://github.com/this-is-tobi/rta-plugins/commit/caebb5505562a3e889f14e5afa5f2d6179c8b56a))
* **s3:** an unsafe-entry refusal counts every entry it refused, and one as one ([3e7a2c7](https://github.com/this-is-tobi/rta-plugins/commit/3e7a2c77c46c2cd0c44ccb62ae8c417d9d65f025))
* **s3:** object get --out and object set receipts give the size in units, one byte included ([a78a62f](https://github.com/this-is-tobi/rta-plugins/commit/a78a62f1d77a5548f202753b86832349cd366439))


### Code Refactoring

* **s3:** each byte count reaches format.Bytes as the integer it arrives as ([6eda8e2](https://github.com/this-is-tobi/rta-plugins/commit/6eda8e2ca15d06ef3d48c0b5638aaaa77f196ac0))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.3.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.14...plugins/s3/v0.3.15) (2026-09-26)


### Code Refactoring

* **s3:** take the tilde rule from the SDK rather than keeping a copy ([c145d00](https://github.com/this-is-tobi/rta-plugins/commit/c145d004fcfed1345204a9b0d3c0869db1904241))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.3.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.13...plugins/s3/v0.3.14) (2026-09-22)


### Bug Fixes

* **s3:** a one-file upload said "would upload 1 files" ([5e307a1](https://github.com/this-is-tobi/rta-plugins/commit/5e307a1b4eafc460d789d36f66cb350de5048bba))


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.3.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.12...plugins/s3/v0.3.13) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.3.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.11...plugins/s3/v0.3.12) (2026-09-20)


### Bug Fixes

* **s3:** a download that could not be closed says so instead of reporting success ([7fbcf8b](https://github.com/this-is-tobi/rta-plugins/commit/7fbcf8bf608e45ea450f20ac5f73bbc8b48223db))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.3.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.10...plugins/s3/v0.3.11) (2026-09-19)


### Bug Fixes

* **s3:** a cancelled or early-stopped call no longer runs on for nobody ([c147fd6](https://github.com/this-is-tobi/rta-plugins/commit/c147fd639f8dc331a9e0a25e0feb200d166fe165))


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.3.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.9...plugins/s3/v0.3.10) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.3.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.8...plugins/s3/v0.3.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.3.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.7...plugins/s3/v0.3.8) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.3.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.6...plugins/s3/v0.3.7) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.3.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.5...plugins/s3/v0.3.6) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.3.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.4...plugins/s3/v0.3.5) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.3.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.3...plugins/s3/v0.3.4) (2026-09-08)


### Bug Fixes

* **s3:** bind the source bucket on every key-scoped capability ([d4af73a](https://github.com/this-is-tobi/rta-plugins/commit/d4af73af4ff9b79974401c0a2c904706716c457c))


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.3.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.2...plugins/s3/v0.3.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.1...plugins/s3/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.3.0...plugins/s3/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.2.1...plugins/s3/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.2.0...plugins/s3/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/s3/v0.1.0...plugins/s3/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))
* the scope a call works in is a flag a profile can carry ([763fe58](https://github.com/this-is-tobi/rta-plugins/commit/763fe58bdcacfd3a677e491af99c8c42ef2a5d3f))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
