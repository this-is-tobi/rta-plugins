# Changelog

## [0.4.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.4.0...plugins/redis/v0.4.1) (2026-10-07)


### Dependencies

* every plugin builds against rta v0.38.0 ([aa66bba](https://github.com/this-is-tobi/rta-plugins/commit/aa66bba2b576abe5439ef4049385a13cacb32d00))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.3.1...plugins/redis/v0.4.0) (2026-10-06)


### Features

* **redis:** an agent reads a short text for the overview, cluster and key.get ([e4cdf41](https://github.com/this-is-tobi/rta-plugins/commit/e4cdf41a8c30e1a65b44724d8109c0051e4c5151))
* **redis:** key.get hands the value back as stored, as the reveal it is ([0956f9d](https://github.com/this-is-tobi/rta-plugins/commit/0956f9daf48248315f356407b104c0c98303ff4f))
* **redis:** search words, examples and the short flags a listing is typed with ([61275ca](https://github.com/this-is-tobi/rta-plugins/commit/61275cafda3c8753aa780b5def81d430c0abdf50))


### Dependencies

* every plugin builds against rta v0.36.0 ([690560f](https://github.com/this-is-tobi/rta-plugins/commit/690560f66a757ffbc219fba35a384725820bfdbb))
* every plugin builds against rta v0.37.0 ([d524fb6](https://github.com/this-is-tobi/rta-plugins/commit/d524fb6f6bbeb64a132b2dbbe007424748539761))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.3.0...plugins/redis/v0.3.1) (2026-10-05)


### Dependencies

* every plugin builds against rta v0.35.0 ([37f4999](https://github.com/this-is-tobi/rta-plugins/commit/37f4999177d1401e0d80a7be47a1caacfcde4845))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.2.0...plugins/redis/v0.3.0) (2026-10-04)


### ⚠ BREAKING CHANGES

* **redis:** the `count` input of redis.slowlog is `limit`, and the `slowlog.count` configuration key is `slowlog.limit`.

### Features

* **redis:** the slow log's row bound is `limit`, as every other row bound is ([cd17f3c](https://github.com/this-is-tobi/rta-plugins/commit/cd17f3c421b61757e488978a90c7dcb787fcb971))


### Bug Fixes

* **redis:** a certificate that does not verify is named for why, macOS's length rule for its fix ([d210a42](https://github.com/this-is-tobi/rta-plugins/commit/d210a42a3b8a1db504288e6e321f0c7ffb0c76a5))
* **redis:** a name that does not draw as itself is listed quoted, written out ([b337fda](https://github.com/this-is-tobi/rta-plugins/commit/b337fda52e4cfcc08f9958520c1445a0d5aca011))
* **redis:** a refusal the server gave names the profile, not the end of its forward ([c299019](https://github.com/this-is-tobi/rta-plugins/commit/c299019af9f1b526378344f09a328602fd1dc2b5))
* **redis:** a TLS refusal the server gave names the profile, not the end of its forward ([d20caef](https://github.com/this-is-tobi/rta-plugins/commit/d20caef00d4d68871c3d19b3dc87da3d1cc96a66))
* **redis:** a Valkey is named for what it is, and its mode is shown ([cc83747](https://github.com/this-is-tobi/rta-plugins/commit/cc837478090a8a24ff4d6b89e996ef3b0ff1de7e))
* **redis:** an ACL user without +ping is told the connection needs it ([7d2b28b](https://github.com/this-is-tobi/rta-plugins/commit/7d2b28b0fe6210908f171444012ca8f819984ba0))
* **redis:** redis.key.get and redis.config.get say what comes back masked, which is on every surface ([d9eeb07](https://github.com/this-is-tobi/rta-plugins/commit/d9eeb07f489f08d6ba57ba290e44fe35925a4202))
* **redis:** redis.key.get stops telling its reader to take `grant.allow` ([7f96dc2](https://github.com/this-is-tobi/rta-plugins/commit/7f96dc2d5da9764456f05aa257a383ba39d6218e))
* **redis:** the calls a message hands over reach the server it came from ([f6da1f5](https://github.com/this-is-tobi/rta-plugins/commit/f6da1f5c9861a2905d35df1c51883bc8eb8d7ef7))
* **redis:** the overview names the profile, not the end of its forward ([35421d8](https://github.com/this-is-tobi/rta-plugins/commit/35421d8f00c5d5dfaf3d5879cd32417ed135ef25))


### Code Refactoring

* **redis:** name the profile, its forward and a certificate's names with the SDK's helpers ([b00f074](https://github.com/this-is-tobi/rta-plugins/commit/b00f074d9a123cee1b8be795606d0f995c63e12f))
* **redis:** the forward's name refusal is the SDK's, not a copy kept by hand ([4008fe9](https://github.com/this-is-tobi/rta-plugins/commit/4008fe9464d9ddeeaf8c4945a63f272052239809))


### Dependencies

* every plugin builds against rta v0.34.0 ([b212d16](https://github.com/this-is-tobi/rta-plugins/commit/b212d165ced0e7aaf112a9be37a56178d950cbe2))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.21...plugins/redis/v0.2.0) (2026-10-03)


### Features

* **redis:** the cluster view shows every node's offset and each replica's distance behind ([6df39a7](https://github.com/this-is-tobi/rta-plugins/commit/6df39a78aff6b7b78ef5ef6f12ca1d3c29bdd711))
* **redis:** the overview says how far behind each replica is and whether it can resume ([33e7e48](https://github.com/this-is-tobi/rta-plugins/commit/33e7e4814ddd82e5e7eb16b10898836d79447662))
* **redis:** tls-server-name checks a forwarded server's certificate for its own name ([0aa32c1](https://github.com/this-is-tobi/rta-plugins/commit/0aa32c1d325d91289966c60ca24e7ae30daad9e6))


### Bug Fixes

* **redis:** a certificate for another name is named as that, not as out of reach ([9dc30ae](https://github.com/this-is-tobi/rta-plugins/commit/9dc30ae8305b4d328fa2e387f98c45ebb98158da))
* **redis:** a certificate is answered with the CA to name by the SDK's reading of its issuer ([1ed032a](https://github.com/this-is-tobi/rta-plugins/commit/1ed032a75d520fcce714a49ba2c117fde96a5c3e))
* **redis:** a name refused through a forward is said to be the forward's, with what gets through ([1a0d255](https://github.com/this-is-tobi/rta-plugins/commit/1a0d2557dad1ecd019aee471aded09a13ab20d09))
* **redis:** a refusal on this machine is named as the port-forward that exited ([e7de3ef](https://github.com/this-is-tobi/rta-plugins/commit/e7de3ef8eb3ede4759a5ecece8dfbcbe5087d0f7))
* **redis:** a setting and its value are spelled by the SDK's naming on every surface ([9732411](https://github.com/this-is-tobi/rta-plugins/commit/9732411553170a1dd30100bcc5b67294969b9f2b))
* **redis:** a TLS server that hangs up through a forward is sent to ca-file, not to tls ([286be7d](https://github.com/this-is-tobi/rta-plugins/commit/286be7d53186e6eb50198651c2e6a9a3cdf21feb))
* **redis:** the listing a missing key offers reaches the server the lookup reached ([84624de](https://github.com/this-is-tobi/rta-plugins/commit/84624de29a7424aa3ffa6b671ce5baf6ff2142f1))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.1.21](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.20...plugins/redis/v0.1.21) (2026-09-29)


### Bug Fixes

* **redis:** a replication peer at an IPv6 address is named [::1]:6380, not ::1:6380 ([86f9e47](https://github.com/this-is-tobi/rta-plugins/commit/86f9e477d3dacdbe2847f94e5476ab7b4f94c820))
* **redis:** ca-file, cert-file and key-file resolve a leading ~, as the other paths do ([d72e5c8](https://github.com/this-is-tobi/rta-plugins/commit/d72e5c84d20d3496a22f769e1cd090847ec3ac1a))


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.1.20](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.19...plugins/redis/v0.1.20) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

## [0.1.19](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.18...plugins/redis/v0.1.19) (2026-09-28)


### Bug Fixes

* **redis:** a ca-file with no PEM certificate says what it must hold, not what it might have been ([1535751](https://github.com/this-is-tobi/rta-plugins/commit/1535751f13bb8e3307cf63b12cff7a02d5c9eed2))
* **redis:** a name DNS cannot resolve is named alone, without the port beside it ([57356e7](https://github.com/this-is-tobi/rta-plugins/commit/57356e773bd97c5aa0f0678f2dbb716b55bd584b))
* **redis:** a password or CA the server wants is named as the operator's setting, never passed ([fe22a12](https://github.com/this-is-tobi/rta-plugins/commit/fe22a126fb1decec7cec12201b063a9ad97a5263))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.1.18](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.17...plugins/redis/v0.1.18) (2026-09-28)


### Bug Fixes

* **redis:** a message names capabilities and inputs the way its reader's surface gives them ([6615aac](https://github.com/this-is-tobi/rta-plugins/commit/6615aac96d7280426e20d37d4d525cfab9484b46))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.1.17](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.16...plugins/redis/v0.1.17) (2026-09-27)


### Bug Fixes

* **redis:** one field, hit, miss or SCAN element is counted in the singular ([d82f4cc](https://github.com/this-is-tobi/rta-plugins/commit/d82f4cc9cc9bf4be5add5a968fc20b387de69568))


### Code Refactoring

* **redis:** each byte count reaches format.Bytes as the integer it arrives as ([2d42fde](https://github.com/this-is-tobi/rta-plugins/commit/2d42fde7a695bfcfd6a14869bedc5d1faa2a509d))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.1.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.15...plugins/redis/v0.1.16) (2026-09-26)


### Bug Fixes

* **redis:** a database past the default sixteen is the server's to refuse ([7c899bc](https://github.com/this-is-tobi/rta-plugins/commit/7c899bcf150f53e82b9141f5d94c44aca6ff6610))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.1.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.14...plugins/redis/v0.1.15) (2026-09-22)


### Bug Fixes

* **redis:** a one-key tree said "1 keys" ([fedafd5](https://github.com/this-is-tobi/rta-plugins/commit/fedafd531d356299cfddc04819f1f3ae34a3976d))


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.1.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.13...plugins/redis/v0.1.14) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.1.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.12...plugins/redis/v0.1.13) (2026-09-20)


### Bug Fixes

* **redis:** a TLS connection is dropped when the caller stops waiting ([28ab621](https://github.com/this-is-tobi/rta-plugins/commit/28ab6217f85348b2530482dd2ad0de18ba6f31d9))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.1.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.11...plugins/redis/v0.1.12) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.1.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.10...plugins/redis/v0.1.11) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.1.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.9...plugins/redis/v0.1.10) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.1.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.8...plugins/redis/v0.1.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.1.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.7...plugins/redis/v0.1.8) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.1.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.6...plugins/redis/v0.1.7) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.1.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.5...plugins/redis/v0.1.6) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.1.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.4...plugins/redis/v0.1.5) (2026-09-08)


### Bug Fixes

* **redis:** mask collection members the same way a scalar value is ([893094b](https://github.com/this-is-tobi/rta-plugins/commit/893094b8c2f9d493908671e5696c27d212211150))


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.1.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.3...plugins/redis/v0.1.4) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.1.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.2...plugins/redis/v0.1.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.1.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.1...plugins/redis/v0.1.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.1.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/redis/v0.1.0...plugins/redis/v0.1.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## 0.1.0 (2026-09-05)


### Features

* **redis:** health, memory, persistence, replication, the keyspace and the slow log ([cfbb458](https://github.com/this-is-tobi/rta-plugins/commit/cfbb458273c94cd370a8987d43697a166a5628f7))


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
