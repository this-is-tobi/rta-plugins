# Changelog

## [0.7.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.6.1...plugins/pg/v0.7.0) (2026-10-06)


### Features

* **pg:** an agent reads a short text for the four capabilities over its budget ([664ee85](https://github.com/this-is-tobi/rta-plugins/commit/664ee85a94aff101df5b79000033f9edd5d8e1ff))
* **pg:** search words, examples and the short flags psql and pg_dump already taught ([26bc8a6](https://github.com/this-is-tobi/rta-plugins/commit/26bc8a685dcc6e68a3f0c07388c711e09dff7694))


### Dependencies

* every plugin builds against rta v0.36.0 ([690560f](https://github.com/this-is-tobi/rta-plugins/commit/690560f66a757ffbc219fba35a384725820bfdbb))
* every plugin builds against rta v0.37.0 ([d524fb6](https://github.com/this-is-tobi/rta-plugins/commit/d524fb6f6bbeb64a132b2dbbe007424748539761))

## [0.6.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.6.0...plugins/pg/v0.6.1) (2026-10-05)


### Code Refactoring

* **pg:** libpq's directory and a client key's mode are read one way, with no Windows branch ([41d6ffa](https://github.com/this-is-tobi/rta-plugins/commit/41d6ffaf8d95d087b64853f89d633cc7277cf44a))


### Dependencies

* every plugin builds against rta v0.35.0 ([37f4999](https://github.com/this-is-tobi/rta-plugins/commit/37f4999177d1401e0d80a7be47a1caacfcde4845))

## [0.6.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.5.0...plugins/pg/v0.6.0) (2026-10-04)


### ⚠ BREAKING CHANGES

* **pg:** sslmode verify-ca or verify-full with ssl-home on is refused when ~/.postgresql/root.crl exists. Move the list aside, or drop ssl-home and name the files.
* **pg:** a client certificate, key or root certificate found under ~/.postgresql is no longer used, by the driver or by the children. Name them with sslcert, sslkey and sslrootcert, or set ssl-home to have rta search libpq's directory.

### Features

* **pg:** sslcert and sslkey name the client certificate, and nothing is read from ~/.postgresql ([6743f34](https://github.com/this-is-tobi/rta-plugins/commit/6743f34d9ea7c1ae4ab84b67b67ea86a7b3a9730))
* **pg:** tls-server-name names what the certificate is checked for, and turns TLS on in a forward ([b146fff](https://github.com/this-is-tobi/rta-plugins/commit/b146fff5724728163f665fbec8a3936964408aca))


### Bug Fixes

* **pg:** a certificate that does not verify is named for why, macOS's length rule for its fix ([bcab8a7](https://github.com/this-is-tobi/rta-plugins/commit/bcab8a7563d0aca6e4609ffa0b1ddacac3ab7a9c))
* **pg:** a column's type named by a stranger is written in SQL's own escape ([41504fe](https://github.com/this-is-tobi/rta-plugins/commit/41504fe788ccc9e1c36e7c3ea5977fddb119b36c))
* **pg:** a dump or a restore over TLS through a kube: forward is refused before it connects ([79e65d7](https://github.com/this-is-tobi/rta-plugins/commit/79e65d7746ddc8a0a80872c3d7fb28d5bec356be))
* **pg:** a name a stranger chose is shown written out when it does not read as itself ([f37de54](https://github.com/this-is-tobi/rta-plugins/commit/f37de54f3e5737e668e10c5383d0b48ee97553ee))
* **pg:** a refusal the server gave names the profile, not the end of its forward ([8288ca9](https://github.com/this-is-tobi/rta-plugins/commit/8288ca96e0c1e569f2be978269a65119b63a8fbb))
* **pg:** a restore and a schema description name the server as the reader reaches it again ([cca3b87](https://github.com/this-is-tobi/rta-plugins/commit/cca3b87efd08763a3c58e5b87f4cd4a3ba8e5058))
* **pg:** a root certificate ssl-home found is named as found, with the way to stop reading it ([cf3bb05](https://github.com/this-is-tobi/rta-plugins/commit/cf3bb05f5add9b97c8d5116ec4d3efb81cd62b93))
* **pg:** a slot given up on says why, and only the reasons about WAL talk about WAL ([26309fb](https://github.com/this-is-tobi/rta-plugins/commit/26309fb157407b70da5e0e7cba074fbffdd22dfd))
* **pg:** a TLS refusal the server gave names the profile, not the end of its forward ([6167c09](https://github.com/this-is-tobi/rta-plugins/commit/6167c098293bb9b0b86ce6e19874a0225f6ab2e7))
* **pg:** fewer synchronous standbys than synchronous_standby_names asks for is a stall ([f798afb](https://github.com/this-is-tobi/rta-plugins/commit/f798afb1cbd9412ad11c7694a7924c9ba9b0a803))
* **pg:** pg.table.list and pg.activity say when they stopped at their limit ([93cd9d9](https://github.com/this-is-tobi/rta-plugins/commit/93cd9d918540525da38ff769f160b4a165b92fd7))
* **pg:** pg.table.list refuses a schema that is not there, as pg.schema.dump does ([173f91b](https://github.com/this-is-tobi/rta-plugins/commit/173f91b1d4d425cdb327d2e5558733e2ae913fd7))
* **pg:** ssl-home beside a revocation list under ~/.postgresql is refused where TLS verifies ([83282af](https://github.com/this-is-tobi/rta-plugins/commit/83282affa4bb17bd12b377bd73686b88f27e9e6f))
* **pg:** the createdb line a missing restore target offers is one word per value to a shell ([89bfb08](https://github.com/this-is-tobi/rta-plugins/commit/89bfb08c6a6eed459d61b3319f0105b953e12ecf))
* **pg:** the descriptions an agent reads stop pointing at calls it cannot make ([f29b92d](https://github.com/this-is-tobi/rta-plugins/commit/f29b92d277cff46bef30eb68d38f507d2fb67efb))
* **pg:** the GRANT the hidden-standby hint offers quotes the role the way SQL does ([5810bbd](https://github.com/this-is-tobi/rta-plugins/commit/5810bbd6c218c54b7ee7d68011ee04d9a16206fd))
* **pg:** the GRANT the hidden-standby hint offers writes an odd role in SQL's own escape ([2d543ca](https://github.com/this-is-tobi/rta-plugins/commit/2d543cabc72ae687bbdc22614985083f0c566506))
* **pg:** the listings and status checks a refusal offers reach the server the refusal came from ([64131b9](https://github.com/this-is-tobi/rta-plugins/commit/64131b94919488293d4a83cebab5a684ab14e0bc))
* **pg:** the policy lookup a hint offers writes an odd table name in SQL's own escape ([0b2c7b2](https://github.com/this-is-tobi/rta-plugins/commit/0b2c7b29ed849f53bee9a6011b39c8324e7e1c6e))
* **pg:** the policy query a row-level-security refusal offers is for this table and quoted ([d66f781](https://github.com/this-is-tobi/rta-plugins/commit/d66f781454318183d515e57e0882be128699aa9f))
* **pg:** the schema description writes an odd name as SQL's own escape ([87941e6](https://github.com/this-is-tobi/rta-plugins/commit/87941e6fa9009151a1c3478acf75ee1eb1f1ca87))
* **pg:** through a forward, a refused certificate's hint stops naming the sslmode the host refuses ([879e7ec](https://github.com/this-is-tobi/rta-plugins/commit/879e7ecb45fc95acb4d94590206d2cb5356cce7c))


### Code Refactoring

* **pg:** name the profile and its forward, and what reaches the server, with the SDK's ([bced140](https://github.com/this-is-tobi/rta-plugins/commit/bced140af305a59bba0e158dddb8239a3e8bb02c))
* **pg:** the certificate-for-another-name refusal is the SDK's, with pg's own code ([06d4e1b](https://github.com/this-is-tobi/rta-plugins/commit/06d4e1b2d4d8290d4a5e0aa12b36a08cea04ca52))
* **pg:** the views read through a querier, so what they show is testable without a server ([38ca336](https://github.com/this-is-tobi/rta-plugins/commit/38ca33674976a6118abaf2c81cee31d2d41e625d))


### Dependencies

* every plugin builds against rta v0.34.0 ([b212d16](https://github.com/this-is-tobi/rta-plugins/commit/b212d165ced0e7aaf112a9be37a56178d950cbe2))

## [0.5.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.4.0...plugins/pg/v0.5.0) (2026-10-03)


### Features

* **pg:** pg.replication shows where every member is and how far behind it is ([fc82dfa](https://github.com/this-is-tobi/rta-plugins/commit/fc82dfad9a65bccfc4e2c9d0f1992fee064d1a6e))
* **pg:** the compact overview carries one line of replication ([27611be](https://github.com/this-is-tobi/rta-plugins/commit/27611be63d738310a6195ee1eca4830451a30f4a))


### Bug Fixes

* **pg:** a dump's restore line reaches the server again by its profile, not a closed forward ([bd844e7](https://github.com/this-is-tobi/rta-plugins/commit/bd844e709e429d22a3070d7b19c1caaeeb060932))
* **pg:** a server that takes a connection over TLS only is named as that, not as a bad password ([b2ac6c0](https://github.com/this-is-tobi/rta-plugins/commit/b2ac6c0fbedfcaeeb2e272224fb744ac563a5895))
* **pg:** a setting, an input and its value are spelled by the SDK's naming on every surface ([2a29f5e](https://github.com/this-is-tobi/rta-plugins/commit/2a29f5e7005ac7423c321813849c115223e9bedc))
* **pg:** only a certificate from an unknown issuer is answered with the CA to name ([ba092bc](https://github.com/this-is-tobi/rta-plugins/commit/ba092bc10b7786997026f42f1a7d16e39f216c5a))


### Dependencies

* every plugin builds against rta v0.33.0 ([52132bb](https://github.com/this-is-tobi/rta-plugins/commit/52132bb83099d3ff1b2299150ce8249b8e345f3e))

## [0.4.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.19...plugins/pg/v0.4.0) (2026-09-29)


### ⚠ BREAKING CHANGES

* **pg:** sslmode verify-ca with no sslrootcert is refused. Name the server's CA in sslrootcert, ~/.postgresql/root.crt when that is where it is, or set sslmode to verify-full to check against this machine's own CAs.
* **pg:** sslrootcert system beside sslmode prefer, require or verify-ca is refused. Set sslmode to verify-full, or name the server's CA as a file.
* **pg:** sslrootcert beside sslmode prefer or require is refused. Set sslmode to verify-ca, which is what require did with a CA, or to verify-full to check the server's name as well.

### Bug Fixes

* **pg:** a certificate macOS does not trust is answered with the CA, not "could not connect" ([5f5ebcc](https://github.com/this-is-tobi/rta-plugins/commit/5f5ebcc743edd305a87fa36bf90b3b6da8e11242))
* **pg:** a dump written by a pg_dump newer than its server says where it restores, and the fix ([96daad6](https://github.com/this-is-tobi/rta-plugins/commit/96daad648fddda3bcb5937b64b8c79b872fe04e0))
* **pg:** a restore into a server older than what wrote it is named as the skew, with the fix ([ae4fd53](https://github.com/this-is-tobi/rta-plugins/commit/ae4fd53b8b6edd79918110462414edff25090fb4))
* **pg:** a restore's missing database is created on the server it reached, port and role named ([3126901](https://github.com/this-is-tobi/rta-plugins/commit/3126901d62207664d036d09e4f7b2a992309d7c7))
* **pg:** an IPv6 server is named [::1]:5432 in messages and receipts, not ::1:5432 ([4e90cb5](https://github.com/this-is-tobi/rta-plugins/commit/4e90cb5b600eaeca369d9d7b89db6bed425cdfc0))
* **pg:** an sslrootcert that cannot be read or holds no certificate is named as that ([2ea70b4](https://github.com/this-is-tobi/rta-plugins/commit/2ea70b4f453548e3baa44e889b74ce6630586e3d))
* **pg:** pg_dump, psql and pg_restore connect within the plugin's bound, and its end is named ([6e97576](https://github.com/this-is-tobi/rta-plugins/commit/6e975769475dad7bbb0d2fde32454ab4f12b9c82))
* **pg:** rejected credentials name where the password is read from, never a variable to set ([b25a4b7](https://github.com/this-is-tobi/rta-plugins/commit/b25a4b7fdabc9ca769a3c22996b9362b1a5fe774))
* **pg:** sslrootcert beside prefer or require is refused, not left inert or implied ([4b94e4d](https://github.com/this-is-tobi/rta-plugins/commit/4b94e4dfe088d96b1ef19e347eb34115d477bf48))
* **pg:** sslrootcert resolves a leading ~ and is made absolute, as the other paths are ([abe43d7](https://github.com/this-is-tobi/rta-plugins/commit/abe43d72b1fc0d219f7ce329349d70ebd9e53779))
* **pg:** sslrootcert system is taken beside verify-full alone, as libpq takes it ([8585293](https://github.com/this-is-tobi/rta-plugins/commit/858529374e55878d1387bf33add5e1beedd04c11))
* **pg:** the connect is bounded, and a host no route reaches is not a port nothing listens on ([79adf2a](https://github.com/this-is-tobi/rta-plugins/commit/79adf2a46d57e31d17fa244e2a9833d832cbcedf))
* **pg:** verify-ca without sslrootcert is refused, not checked against the system store ([2e1ce15](https://github.com/this-is-tobi/rta-plugins/commit/2e1ce1584678470e9148647913307d663d55c4db))


### Dependencies

* every plugin builds against rta v0.32.0 ([c9b5ed6](https://github.com/this-is-tobi/rta-plugins/commit/c9b5ed66ef7bf7b825878b8d7307ade9963cc294))

## [0.3.19](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.18...plugins/pg/v0.3.19) (2026-09-28)


### Dependencies

* every plugin builds against rta v0.31.0 ([635cbe4](https://github.com/this-is-tobi/rta-plugins/commit/635cbe46053dbb20f598fa57ee30d5fc55231222))

## [0.3.18](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.17...plugins/pg/v0.3.18) (2026-09-28)


### Bug Fixes

* **pg:** an untrusted certificate names sslrootcert as the operator's setting, never as passed ([f2b535f](https://github.com/this-is-tobi/rta-plugins/commit/f2b535f08ac192b0171594de551d0f38243c55c6))
* **pg:** the dump receipt's restore line keeps the TLS the dump was taken over ([e949b8b](https://github.com/this-is-tobi/rta-plugins/commit/e949b8b6522ee01e51c0de24d12386d687252fba))


### Dependencies

* every plugin builds against rta v0.30.0 ([df1097a](https://github.com/this-is-tobi/rta-plugins/commit/df1097ad5b38f9d45c1f7efd4dd48a941b9e62d8))

## [0.3.17](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.16...plugins/pg/v0.3.17) (2026-09-28)


### Bug Fixes

* **pg:** a flag of pg_dump or its clients is named with its program, never bare ([d7122ea](https://github.com/this-is-tobi/rta-plugins/commit/d7122eaa0917cd0252fcd6e1bbe71147c0a8c3e3))
* **pg:** a message names capabilities and inputs the way its reader's surface gives them ([023da97](https://github.com/this-is-tobi/rta-plugins/commit/023da979c92e276c8dcba3c76298c69f342f71ec))


### Dependencies

* every plugin builds against rta v0.29.0 ([af6b7e9](https://github.com/this-is-tobi/rta-plugins/commit/af6b7e93f56ab6311467c0c7bfe64f4728d41486))

## [0.3.16](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.15...plugins/pg/v0.3.16) (2026-09-27)


### Bug Fixes

* **pg:** a limit of one row is refused as "more than 1 row" ([28a85fe](https://github.com/this-is-tobi/rta-plugins/commit/28a85fe4b82712ea5c95b6dbf0a3ecce488b460d))


### Code Refactoring

* **pg:** each byte count reaches format.Bytes as the integer it arrives as ([b8f17a5](https://github.com/this-is-tobi/rta-plugins/commit/b8f17a5b14384fbe0adf4400fbf1f426ed892189))


### Dependencies

* every plugin builds against rta v0.28.0 ([c75c7cb](https://github.com/this-is-tobi/rta-plugins/commit/c75c7cbb87beb645ace0f47544050f3dfab26d84))

## [0.3.15](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.14...plugins/pg/v0.3.15) (2026-09-26)


### Code Refactoring

* **pg:** answer three findings, none of them a defect ([50643ac](https://github.com/this-is-tobi/rta-plugins/commit/50643acf5b44daf260a0558bc436cadb2e908699))
* **pg:** take the tilde rule from the SDK rather than keeping a copy ([d735ee1](https://github.com/this-is-tobi/rta-plugins/commit/d735ee148c7304203605d01a1ab08e34f044db3c))


### Dependencies

* every plugin builds against rta v0.27.0 ([edbb623](https://github.com/this-is-tobi/rta-plugins/commit/edbb623be49fbb2eb4fe0d64f120a05a33447375))

## [0.3.14](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.13...plugins/pg/v0.3.14) (2026-09-22)


### Bug Fixes

* **pg:** a one-relation database said "1 relations" ([a389382](https://github.com/this-is-tobi/rta-plugins/commit/a3893822b5d3186fcad33ed8a123e7ae68451cfa))


### Dependencies

* every plugin builds against rta v0.26.0 ([0d9cc23](https://github.com/this-is-tobi/rta-plugins/commit/0d9cc23380589dd053184cc810060677d394d29a))

## [0.3.13](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.12...plugins/pg/v0.3.13) (2026-09-21)


### Dependencies

* every plugin builds against rta v0.24.0 ([ea45517](https://github.com/this-is-tobi/rta-plugins/commit/ea455173a5b24222c85fb9e46acc8b9d109af76e))
* every plugin builds against rta v0.25.0 ([85e0797](https://github.com/this-is-tobi/rta-plugins/commit/85e07975746df2fba744f54ef3371944e09995a2))

## [0.3.12](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.11...plugins/pg/v0.3.12) (2026-09-20)


### Bug Fixes

* **pg:** what this role cannot see into says so, instead of reading as zero and empty ([182094c](https://github.com/this-is-tobi/rta-plugins/commit/182094c9ee5e869d7f823f5e30eae62906e7ee92))


### Dependencies

* every plugin builds against rta v0.23.0 ([57431e7](https://github.com/this-is-tobi/rta-plugins/commit/57431e7e41a9e6d04ad1b958d1591152bae47ccb))

## [0.3.11](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.10...plugins/pg/v0.3.11) (2026-09-19)


### Dependencies

* every plugin builds against rta v0.22.0 ([58621b2](https://github.com/this-is-tobi/rta-plugins/commit/58621b2ad2a0ae86e56f727d470926464910dd41))

## [0.3.10](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.9...plugins/pg/v0.3.10) (2026-09-16)


### Dependencies

* every plugin builds against rta v0.21.1 ([9640323](https://github.com/this-is-tobi/rta-plugins/commit/96403231818ff10806ed0ca85f700620aa1fdfbd))

## [0.3.9](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.8...plugins/pg/v0.3.9) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.20.0 ([3c19f27](https://github.com/this-is-tobi/rta-plugins/commit/3c19f27c685ad039754bff6ffeaf458abf0ac4c9))

## [0.3.8](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.7...plugins/pg/v0.3.8) (2026-09-14)


### Dependencies

* every plugin builds against rta v0.19.0 ([d034909](https://github.com/this-is-tobi/rta-plugins/commit/d0349098fc102306562ce8223ba4dbda6c3146d1))

## [0.3.7](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.6...plugins/pg/v0.3.7) (2026-09-13)


### Dependencies

* every plugin builds against rta v0.18.0 ([bc1a072](https://github.com/this-is-tobi/rta-plugins/commit/bc1a0726f923dea40b71355ffabb5395eef9f827))

## [0.3.6](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.5...plugins/pg/v0.3.6) (2026-09-11)


### Dependencies

* every plugin builds against rta v0.17.0 ([0280b69](https://github.com/this-is-tobi/rta-plugins/commit/0280b699951f4b1c85a5c125f8c01789a65062a5))

## [0.3.5](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.4...plugins/pg/v0.3.5) (2026-09-09)


### Dependencies

* every plugin builds against rta v0.16.0 ([587bb90](https://github.com/this-is-tobi/rta-plugins/commit/587bb9059aa623bab8a88a9b5556f0c6f5320037))

## [0.3.4](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.3...plugins/pg/v0.3.4) (2026-09-08)


### Dependencies

* every plugin builds against rta v0.15.0 ([a8203b7](https://github.com/this-is-tobi/rta-plugins/commit/a8203b7c0377c3c9440dd1940a34d74679840270))

## [0.3.3](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.2...plugins/pg/v0.3.3) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.14.0 ([4165a34](https://github.com/this-is-tobi/rta-plugins/commit/4165a3402bcbcbb4065a292d74942ebaa96950b2))

## [0.3.2](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.1...plugins/pg/v0.3.2) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.13.0 ([5b90f0f](https://github.com/this-is-tobi/rta-plugins/commit/5b90f0fa7f7ab18769820df68d51cee0041fe3cd))

## [0.3.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.3.0...plugins/pg/v0.3.1) (2026-09-06)


### Dependencies

* every plugin builds against rta v0.12.0 ([887d933](https://github.com/this-is-tobi/rta-plugins/commit/887d933329fad6206abdcd576ebe509915c46931))

## [0.3.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.2.1...plugins/pg/v0.3.0) (2026-09-06)


### Features

* what moves a whole store, or mints an identity, is declared for the person at the terminal ([858d399](https://github.com/this-is-tobi/rta-plugins/commit/858d3998105b6799a534522daf3114bc0c80126d))


### Dependencies

* every plugin builds against rta v0.11.0 ([e2a63c9](https://github.com/this-is-tobi/rta-plugins/commit/e2a63c9e32dfa8f8a969ea47ee77a8a5540af691))

## [0.2.1](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.2.0...plugins/pg/v0.2.1) (2026-09-05)


### Dependencies

* every plugin builds against rta v0.10.0 ([f3dd4b3](https://github.com/this-is-tobi/rta-plugins/commit/f3dd4b32fe9ac781906511658718d80329192f07))
* every plugin builds against rta v0.9.0 ([55d5047](https://github.com/this-is-tobi/rta-plugins/commit/55d504748e26a08aa25bcb300d90a136fbab7395))

## [0.2.0](https://github.com/this-is-tobi/rta-plugins/compare/plugins/pg/v0.1.0...plugins/pg/v0.2.0) (2026-09-05)


### Features

* every preference an operator would set once is a config key ([a9f77b0](https://github.com/this-is-tobi/rta-plugins/commit/a9f77b0cd0f77ed080809d576e9a09027a0913ec))


### Dependencies

* every plugin builds against rta at its own name ([cc611ce](https://github.com/this-is-tobi/rta-plugins/commit/cc611ce000918ec4309ee33cfc6e4ee8315c69cd))

## 0.1.0 (2026-09-04)


### Dependencies

* the plugins become modules of this repository, pinned to rta v0.6.0 ([0428e9d](https://github.com/this-is-tobi/rta-plugins/commit/0428e9d8fda18d74bc1e76ecf1ffb5d5469d853c))
