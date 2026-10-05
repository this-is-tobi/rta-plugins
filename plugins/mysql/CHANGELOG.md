# Changelog

## [0.6.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.6.0...plugins/mysql/v0.6.1) (2026-10-05)


### Code Refactoring

* **mysql,mariadb:** the client's errno is this platform's own, with no Windows skip in tests ([4f772d3](https://github.com/this-is-tobi/rta-plugins/commit/4f772d36550ef30d017172468bbdc19e99a1f08b))


### Dependencies

* every plugin builds against rta v0.35.0 ([37f4999](https://github.com/this-is-tobi/rta-plugins/commit/37f4999177d1401e0d80a7be47a1caacfcde4845))

## [0.6.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.5.0...plugins/mysql/v0.6.0) (2026-10-04)


### Features

* **mysql:** a CA or a name to check reaches a server that insists on TLS through a forward ([b649396](https://github.com/this-is-tobi/rta-plugins/commit/b649396deed253ec04bcb81a299ea10735057309))


### Bug Fixes

* **mysql:** a certificate that does not verify is named for why, macOS's length rule for its fix ([bb73eaf](https://github.com/this-is-tobi/rta-plugins/commit/bb73eaf860c0073c112ae056f5d713695725961c))
* **mysql:** a certificate the server presented and nothing accepts names the profile ([6695be7](https://github.com/this-is-tobi/rta-plugins/commit/6695be7779b4f1e064d59d1ee6f032b55ba3f969))
* **mysql:** a listed name that does not draw as itself is shown quoted, not cleaned ([d78e7f7](https://github.com/this-is-tobi/rta-plugins/commit/d78e7f74c947296fc67854347c4767513cbbd67d))
* **mysql:** a long statement in the activity listing is cut between characters, not through one ([64496ff](https://github.com/this-is-tobi/rta-plugins/commit/64496ffc23bef632e941e5fc333706fb901726ae))
* **mysql:** a nameless certificate the system refuses for its length is not sent to verify-ca ([115da6e](https://github.com/this-is-tobi/rta-plugins/commit/115da6e6fd00a6beaf528f7bd6e6eec797ad9428))
* **mysql:** a refusal the server gave names the profile, not the end of its forward ([9b11369](https://github.com/this-is-tobi/rta-plugins/commit/9b11369beaa0210a5178b0304751f2b240362aa4))
* **mysql:** a rejected statement is not blamed on the connection; no database says what to do ([52d6eb7](https://github.com/this-is-tobi/rta-plugins/commit/52d6eb7a0dac476bf0f6bea9ec7d5541914dae79))
* **mysql:** a restore names the server as the reader reaches it again, not a forward's end ([d738bcc](https://github.com/this-is-tobi/rta-plugins/commit/d738bcc7ceb373878936d772f4de70097718dcca))
* **mysql:** a revoked certificate that names no host keeps the system's verdict, not verify-ca ([28156aa](https://github.com/this-is-tobi/rta-plugins/commit/28156aa7ea463ab7ee1550beb224e5b353f4c8a1))
* **mysql:** a schema-wide grant on a name with an underscore is not called narrow ([48bbac9](https://github.com/this-is-tobi/rta-plugins/commit/48bbac9c043fa2395687590b25a53fafc9ecb8dc))
* **mysql:** a session's time and the server's uptime read "1h", not "1h0m0s" ([2cdeeb9](https://github.com/this-is-tobi/rta-plugins/commit/2cdeeb97975acff2e55fdbd9da0de64cd86cc57f))
* **mysql:** mysql.query stops telling its reader to take `grant.allow` ([5d5189b](https://github.com/this-is-tobi/rta-plugins/commit/5d5189b77c2af3af73c526044538acdfebbff15d))
* **mysql:** the CREATE DATABASE a missing restore target offers quotes a name that needs it ([dcdfda2](https://github.com/this-is-tobi/rta-plugins/commit/dcdfda2963fd852c0171a815ed1ac236ac057d7a))
* **mysql:** the database, table and session listings say when they stopped at their limit ([9000a95](https://github.com/this-is-tobi/rta-plugins/commit/9000a952d3bc3d9a1c832d71559874fdead87a7d))
* **mysql:** the listings and status checks a refusal offers reach the server the refusal came from ([264f89e](https://github.com/this-is-tobi/rta-plugins/commit/264f89ec93fdcbaff79ae8ad445f8f1c3ffcfae2))
* **mysql:** the status page names the profile, not the end of its forward ([d47e4f4](https://github.com/this-is-tobi/rta-plugins/commit/d47e4f458b4406d6350715ce95f0f699361be0dd))


### Code Refactoring

* **mysql:** name the profile, its forward and a certificate's names with the SDK's helpers ([ef6ed52](https://github.com/this-is-tobi/rta-plugins/commit/ef6ed52bf919e82fd2c5d88d37f1d59a1c4374c2))
* **mysql:** the refusal for a certificate checked for a forward's end is the SDK's ([cd4785f](https://github.com/this-is-tobi/rta-plugins/commit/cd4785fdbc402058dcd2fd9deff406ce7a8f1884))


### Dependencies

* every plugin builds against rta v0.34.0 ([b212d16](https://github.com/this-is-tobi/rta-plugins/commit/b212d165ced0e7aaf112a9be37a56178d950cbe2))

## [0.5.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.4.0...plugins/mysql/v0.5.0) (2026-10-03)


### Features

* **mysql:** replication.status reads group replication membership and the majority ([ab8a10e](https://github.com/this-is-tobi/rta-plugins/commit/ab8a10eabc001d0fccb11e6b2e5e4b2c18d6ce69))
* **mysql:** replication.status shows role, lag, positions and replicas in one view ([732026a](https://github.com/this-is-tobi/rta-plugins/commit/732026a0b73d25ac3048c74e3729d9e3badd90b9))


### Bug Fixes

* **mysql:** a client that found no route to the server is named as that, not as refused ([266f969](https://github.com/this-is-tobi/rta-plugins/commit/266f969a47a751cec09f8bb892788ad5fed1f34f))
* **mysql:** a dump's restore line reaches the server again by its profile, not a closed forward ([338737c](https://github.com/this-is-tobi/rta-plugins/commit/338737c9c608adb63ae86045154a180ea35693d7))
* **mysql:** a server that insists on TLS through a forward is not sent to tls, which opens none ([fadb0c2](https://github.com/this-is-tobi/rta-plugins/commit/fadb0c2b51146a5b7153207df9d55f2456ad682d))
* **mysql:** a setting, an input and its value are spelled by the SDK's naming on every surface ([bb247c0](https://github.com/this-is-tobi/rta-plugins/commit/bb247c0c63f95994b0a8c93102d6d73ffc733bed))
* **mysql:** only a certificate from an unknown issuer is answered with the CA to name ([9da93ac](https://github.com/this-is-tobi/rta-plugins/commit/9da93ac0167759c30968bb2f58d0679227182a84))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.19...plugins/mysql/v0.4.0) (2026-09-29)


### Features

* **mysql:** a server behind a private CA is verified against ca-file, rather than not at all ([ed5536e](https://github.com/this-is-tobi/rta-plugins/commit/ed5536e45426ef5fcb3a1a6345a6eae39587bef3))
* **mysql:** tls verify-ca checks the server's chain against ca-file and not its name ([8e518e1](https://github.com/this-is-tobi/rta-plugins/commit/8e518e1a6d92583714663297184361e81df14605))


### Bug Fixes

* **mysql:** a certificate for another host, or for none, is named as that, not as out of reach ([40d6da8](https://github.com/this-is-tobi/rta-plugins/commit/40d6da8716d7f4ea741609ab4356b355768abae8))
* **mysql:** a dump or restore over tls true with no CA names ca-file as what the client needs ([dd9f8a4](https://github.com/this-is-tobi/rta-plugins/commit/dd9f8a4e56d22ebb1ba0e2f20f520f00e61c18a2))
* **mysql:** a dump or restore reaches the server its pre-flight checked, over TCP, not a socket ([c0a28df](https://github.com/this-is-tobi/rta-plugins/commit/c0a28dfc0293cb4e8f6c670359be6fa552701506))
* **mysql:** a restore names an IPv6 server [::1]:3306, not ::1:3306 ([36c9a1e](https://github.com/this-is-tobi/rta-plugins/commit/36c9a1e4b6bbde25b03c3791fe793cb66f0d037f))
* **mysql:** a server that insists on TLS, or offers none, is named as that, not as a failed query ([a8ab306](https://github.com/this-is-tobi/rta-plugins/commit/a8ab3064472188b0a3535c4c5d9a2f5bc6bdd7be))
* **mysql:** an IPv6 host is dialled as the address it is, not looked up as a name ([7357bb8](https://github.com/this-is-tobi/rta-plugins/commit/7357bb8bb2df8bbf44e4d1498d84177fd783e147))
* **mysql:** rejected credentials name where the password is read from, never a variable to set ([efea969](https://github.com/this-is-tobi/rta-plugins/commit/efea96962f65ceca901dca803eaf578f109c3eab))
* **mysql:** the restore's client connects within the plugin's bound, and a timeout is named ([c738ae8](https://github.com/this-is-tobi/rta-plugins/commit/c738ae84ca12c53987a2bd0e909c90944b89e1bd))


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.3.19](https://github.com/this-is-tobi/rta-plugins/compare/plugins/mysql/v0.3.18...plugins/mysql/v0.3.19) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

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
