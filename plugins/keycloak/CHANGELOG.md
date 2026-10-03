# Changelog

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.13...plugins/keycloak/v0.2.0) (2026-10-03)


### Features

* **keycloak:** tls-server-name checks a forwarded server's certificate for its own name ([44e244b](https://github.com/this-is-tobi/rta-plugins/commit/44e244b54b06abd70ec9734210c62086c93e5c15))


### Bug Fixes

* **keycloak:** a certificate is read before the dial, whatever names the certificate holds ([a41a347](https://github.com/this-is-tobi/rta-plugins/commit/a41a347e2c67b615fe86f0a23e43d4911708955c))
* **keycloak:** a hang-up on plain HTTP names the scheme and where it is changed ([675b385](https://github.com/this-is-tobi/rta-plugins/commit/675b385a105e3b4d4e3ca07e90412ec2f83cb8fc))
* **keycloak:** a listing a refusal offers reads the realm the call read ([f3e2c6c](https://github.com/this-is-tobi/rta-plugins/commit/f3e2c6c6e60af7a02421b3ad7ed71456880e5dde))
* **keycloak:** a refused or missing secret names the variable the call read it from ([2136791](https://github.com/this-is-tobi/rta-plugins/commit/21367916854b043384634c9f8efd8c4472d3fbf3))
* **keycloak:** only a certificate no CA here vouches for is answered with the CA file to name ([61a7718](https://github.com/this-is-tobi/rta-plugins/commit/61a771830bbae034975c8b3225edd8e7263c7c3d))


### Code Refactoring

* **keycloak:** hints name settings, the explain page and a DNS lookup in the SDK's words ([04e7c5d](https://github.com/this-is-tobi/rta-plugins/commit/04e7c5d6eec2fa6f90be3d856a43a29cb82eb478))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.1.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.12...plugins/keycloak/v0.1.13) (2026-09-29)


### Bug Fixes

* **keycloak:** a ca-file that cannot be used says what it must hold, named as its reader sets it ([8ac268e](https://github.com/this-is-tobi/rta-plugins/commit/8ac268ed06c6bd8d30ca9d5a7fa9c13ad8f7b0f3))
* **keycloak:** a certificate macOS does not trust is answered with the CA, not "could not reach" ([f66feed](https://github.com/this-is-tobi/rta-plugins/commit/f66feed10b555f74842993d0f6c143ceb9bc1309))
* **keycloak:** a server no route reaches is named as unreachable, not as a port nothing hears ([1afd0fe](https://github.com/this-is-tobi/rta-plugins/commit/1afd0fea768097621cf861329c31d6079b6e19cb))
* **keycloak:** an untrusted certificate names ca-file as the operator's setting, never as passed ([e25e992](https://github.com/this-is-tobi/rta-plugins/commit/e25e99271222341c2cc87600800d0ef148ba0c91))
* **keycloak:** ca-file resolves a leading ~, as the other paths do ([aecfc99](https://github.com/this-is-tobi/rta-plugins/commit/aecfc99669998f5fa8cbe58f13f5f41dbad68769))
* **keycloak:** the token and connection refusals name each setting as their reader changes it ([062f2f4](https://github.com/this-is-tobi/rta-plugins/commit/062f2f428c7e9fa3d212a16b8bb53ba1ccf62abf))


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.1.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.11...plugins/keycloak/v0.1.12) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

## [0.1.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.10...plugins/keycloak/v0.1.11) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.1.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.9...plugins/keycloak/v0.1.10) (2026-09-28)


### Bug Fixes

* **keycloak:** a message names capabilities and inputs the way its reader's surface gives them ([17b5064](https://github.com/this-is-tobi/rta-plugins/commit/17b50641c33aba74c28dc72f82e4920da80d4851))
* **keycloak:** a name DNS cannot resolve is reported as that, not as nothing listening ([9b25c58](https://github.com/this-is-tobi/rta-plugins/commit/9b25c5893a4a945ae846953288ce9404440a9cf0))
* **keycloak:** an MFA coverage line over one enabled user counts it in the singular ([c11f53e](https://github.com/this-is-tobi/rta-plugins/commit/c11f53ec0546960b4fa3914c48e24e8495969421))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.1.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.8...plugins/keycloak/v0.1.9) (2026-09-27)


### Bug Fixes

* **keycloak:** a lockout, reuse or audit bound of one is counted in the singular ([8f09dc9](https://github.com/this-is-tobi/rta-plugins/commit/8f09dc97ccdbede686bd854610df22dea0ec0f9d))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.1.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.7...plugins/keycloak/v0.1.8) (2026-09-26)


### Code Refactoring

* **keycloak:** a tagged switch on the count ([f743aef](https://github.com/this-is-tobi/rta-plugins/commit/f743aef33808c5ee1e214266609b2aa689ceed17))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.1.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.6...plugins/keycloak/v0.1.7) (2026-09-22)


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.1.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.5...plugins/keycloak/v0.1.6) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.1.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.4...plugins/keycloak/v0.1.5) (2026-09-20)


### Bug Fixes

* **keycloak:** a check the audit could not run is reported, not counted as clean ([0a06215](https://github.com/this-is-tobi/rta-plugins/commit/0a0621548c25c0148618bb9228b920c45f1e9edc))


### Code Refactoring

* **keycloak:** drop a type and a helper nothing calls ([259dcb1](https://github.com/this-is-tobi/rta-plugins/commit/259dcb17fb9e4c49aa7c4e89f1c76026992a800d))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.1.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.3...plugins/keycloak/v0.1.4) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.1.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.2...plugins/keycloak/v0.1.3) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.1.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.1...plugins/keycloak/v0.1.2) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.1.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/keycloak/v0.1.0...plugins/keycloak/v0.1.1) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## 0.1.0 (2026-09-13)


### Features

* **keycloak:** users, clients, flows, sessions, events, and a realm audited against named controls ([13ea187](https://github.com/this-is-tobi/rta-plugins/commit/13ea187c9e0e797c853aa80c4415cf9d04614c73))
