# Changelog

## [0.5.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.4.0...plugins/mariadb/v0.5.0) (2026-10-03)


### ⚠ BREAKING CHANGES

* **mariadb:** mariadb.galera.status is removed; mariadb.replication.status answers it, with the same verdicts as grade words and the cluster state uuid, last committed sequence, members and queues beside them. A grant or tool entry naming the old capability no longer matches.

### Features

* **mariadb:** replication.status carries role, lag, GTID positions, replicas and Galera state ([b786602](https://github.com/this-is-tobi/rta-plugins/commit/b786602e955cf9e6f79f31b38394b20fe80b10d0))


### Bug Fixes

* **mariadb:** a client that found no route to the server is named as that, not as refused ([6bcc618](https://github.com/this-is-tobi/rta-plugins/commit/6bcc618b9943b1342109e7b1716b64fb68904c94))
* **mariadb:** a dump's restore line reaches the server again by its profile, not a closed forward ([1c8474a](https://github.com/this-is-tobi/rta-plugins/commit/1c8474ac4689be58120b0cc82c9ee1d0f2793da2))
* **mariadb:** a server that insists on TLS through a forward is not sent to tls, which opens none ([c575d69](https://github.com/this-is-tobi/rta-plugins/commit/c575d69b606258154e7975c12b14c9f8ee9c8d4c))
* **mariadb:** a setting, an input and its value are spelled by the SDK's naming on every surface ([4dbcfe5](https://github.com/this-is-tobi/rta-plugins/commit/4dbcfe5755cbacc4facf231c114363051979bc07))
* **mariadb:** only a certificate from an unknown issuer is answered with the CA to name ([d84bbc6](https://github.com/this-is-tobi/rta-plugins/commit/d84bbc6fc2f1ec5c66e3a16e647a8c52b4eb1e8f))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.19...plugins/mariadb/v0.4.0) (2026-09-29)


### Features

* **mariadb:** a server behind a private CA is verified against ca-file, rather than not at all ([840f2a7](https://github.com/this-is-tobi/rta-plugins/commit/840f2a75cef182729810e0f5213793462e1014f5))
* **mariadb:** tls verify-ca checks the server's chain against ca-file and not its name ([494cc7f](https://github.com/this-is-tobi/rta-plugins/commit/494cc7f7193d72ac0622462e7dcba1e7e3fa08a2))


### Bug Fixes

* **mariadb:** a certificate for another host, or for none, is named as that, not as out of reach ([4b7d503](https://github.com/this-is-tobi/rta-plugins/commit/4b7d5032ae7776bbbd4065a264c154bdea82f2fc))
* **mariadb:** a dump or restore reaches the server its pre-flight checked, over TCP, not a socket ([e1558cd](https://github.com/this-is-tobi/rta-plugins/commit/e1558cd25d645686f8ce1fbfe5ad53880695e2d8))
* **mariadb:** a restore names an IPv6 server [::1]:3306, not ::1:3306 ([9be16fa](https://github.com/this-is-tobi/rta-plugins/commit/9be16fa185fae46f179cf33cbbbc1dd69f8b62c2))
* **mariadb:** a server that insists on TLS, or offers none, is named as that, not as a failed query ([7ecb862](https://github.com/this-is-tobi/rta-plugins/commit/7ecb8625fedb0d3d31e0f41b53feb9046cd335aa))
* **mariadb:** an IPv6 host is dialled as the address it is, not looked up as a name ([13d530f](https://github.com/this-is-tobi/rta-plugins/commit/13d530f347705b057527bad6bab202db1ef7b17a))
* **mariadb:** rejected credentials name where the password is read from, never a variable to set ([2b0fde9](https://github.com/this-is-tobi/rta-plugins/commit/2b0fde96dd5a31e336805c58c486a4b96391c9b7))
* **mariadb:** the restore's client connects within the plugin's bound, and a timeout is named ([fe30b3b](https://github.com/this-is-tobi/rta-plugins/commit/fe30b3be9bfbb91d1599309f44c7400b8684d6c7))
* **mariadb:** the server this plugin talks to is called MariaDB, not the other fork's name ([1944a2e](https://github.com/this-is-tobi/rta-plugins/commit/1944a2ef3b12c10f5f65024890a373778b71a1a4))


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.3.19](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.18...plugins/mariadb/v0.3.19) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

## [0.3.18](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.17...plugins/mariadb/v0.3.18) (2026-09-28)


### Bug Fixes

* **mariadb:** the dump receipt's restore line keeps the TLS the dump was taken over ([e935129](https://github.com/this-is-tobi/rta-plugins/commit/e93512941d4b53e7fbcbace2f6e2e3f993aa14ff))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.3.17](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.16...plugins/mariadb/v0.3.17) (2026-09-28)


### Bug Fixes

* **mariadb:** a flag of mariadb-dump, the client or the server is named with its program ([067646c](https://github.com/this-is-tobi/rta-plugins/commit/067646cabc717746a91e5fb06784003a3cbd0302))
* **mariadb:** a message names capabilities and inputs the way its reader's surface gives them ([9f5cfb5](https://github.com/this-is-tobi/rta-plugins/commit/9f5cfb568c3e01e42d9503c5e586f51e83a3b2c8))
* **mariadb:** a name DNS cannot resolve is reported as that, not as nothing listening ([3dbf812](https://github.com/this-is-tobi/rta-plugins/commit/3dbf812667d95f01a429a5986f24299828d4741e))
* **mariadb:** a split Galera node's receipt points at mariadb.galera.status, which rta has ([a352318](https://github.com/this-is-tobi/rta-plugins/commit/a352318df9c890eb541a7e1189cb87fcb7c7365c))
* **mariadb:** the query's description says it needs a grant, as every write now does ([d215cb7](https://github.com/this-is-tobi/rta-plugins/commit/d215cb7bb5373e5a1ffd0827a6c431c053446a53))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.3.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.15...plugins/mariadb/v0.3.16) (2026-09-27)


### Bug Fixes

* **mariadb:** a dump receipt counts one table read live as "1 non-transactional table was" ([e336d05](https://github.com/this-is-tobi/rta-plugins/commit/e336d05185ea6a6afa13ed67bcf41ac8ae912821))
* **mariadb:** a limit that leaves one table or row out counts it in the singular ([187e394](https://github.com/this-is-tobi/rta-plugins/commit/187e394deee1a37a76fc9de92d81640a4e300ea4))


### Code Refactoring

* **mariadb:** each byte count reaches format.Bytes as the integer it arrives as ([ec40593](https://github.com/this-is-tobi/rta-plugins/commit/ec4059333864c5f85259eafdcb107ae8ae95d91c))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.3.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.14...plugins/mariadb/v0.3.15) (2026-09-26)


### Code Refactoring

* **mariadb:** take the tilde rule from the SDK rather than keeping a copy ([49cbcbc](https://github.com/this-is-tobi/rta-plugins/commit/49cbcbcc54056b6169a72a7dc93443b14c18c041))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.3.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.13...plugins/mariadb/v0.3.14) (2026-09-22)


### Bug Fixes

* **mariadb:** a one-table schema said "1 tables" ([c6cc17c](https://github.com/this-is-tobi/rta-plugins/commit/c6cc17c95c6778e6106b4d92ca0ead2954b0d3ed))


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.3.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.12...plugins/mariadb/v0.3.13) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.3.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.11...plugins/mariadb/v0.3.12) (2026-09-20)


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.3.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.10...plugins/mariadb/v0.3.11) (2026-09-19)


### Bug Fixes

* **mariadb:** the activity description names this plugin's own overview ([cc350ea](https://github.com/this-is-tobi/rta-plugins/commit/cc350eaa08b4573f781f1c5bd6b6db885336d642))


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.3.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.9...plugins/mariadb/v0.3.10) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.3.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.8...plugins/mariadb/v0.3.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.3.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.7...plugins/mariadb/v0.3.8) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.3.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.6...plugins/mariadb/v0.3.7) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.3.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.5...plugins/mariadb/v0.3.6) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.3.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.4...plugins/mariadb/v0.3.5) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.3.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.3...plugins/mariadb/v0.3.4) (2026-09-08)


### Bug Fixes

* **mariadb:** drop the failing statement out of replication.status ([bfbcbd9](https://github.com/this-is-tobi/rta-plugins/commit/bfbcbd99d5b7904868ff816541623f4750c8ddd4))


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.3.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.2...plugins/mariadb/v0.3.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.1...plugins/mariadb/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.3.0...plugins/mariadb/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.2.1...plugins/mariadb/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.2.0...plugins/mariadb/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mariadb/v0.1.0...plugins/mariadb/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
