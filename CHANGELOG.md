## [1.1.0-alpha.1](https://github.com/weka/weka-operator/compare/v1.0.0...v1.1.0-alpha.1) (2024-08-27)

### Features

* allow no redundancy for weka cluster ([7e07d4b](https://github.com/weka/weka-operator/commit/7e07d4bfccbd37b8e8a3a340badfd89325f14c27))
* break out from topology ([91a5806](https://github.com/weka/weka-operator/commit/91a5806d412100eb01d7632e436bc000c78c1396))
* semrel releases ([4f14a8d](https://github.com/weka/weka-operator/commit/4f14a8d525556920ff7c7e6880e3351a609bf7a1))
* support different node selectors for compute/drive and s3 ([1375275](https://github.com/weka/weka-operator/commit/13752758adc4b55c63ab172060533db2d2ebe5de))
* support signing all drives ([db9f57b](https://github.com/weka/weka-operator/commit/db9f57b1268dea799d12dcd4968774d84a4e1a6c))
* support signing all drives ([3e37795](https://github.com/weka/weka-operator/commit/3e3779518e64738f71894b05f6a438578f5a4dc0))

### Bug Fixes

* add caching to workflow, add secrets use to semantic release ([e638b5f](https://github.com/weka/weka-operator/commit/e638b5fd84845baa457816316f588402992665a7))
* add semantic-release exec into package.json ([9a59608](https://github.com/weka/weka-operator/commit/9a596082695c773d33da8b9a1f920fc0f268f645))
* bad yaml formatting ([e4b143b](https://github.com/weka/weka-operator/commit/e4b143b24468a4879aac00aef14f07a6f1e0313b))
* build: frozen go version ([6ce14e9](https://github.com/weka/weka-operator/commit/6ce14e9ca6a87908704156621c98c9a51f05f1ac))
* change releaes create shell to bash ([192ede1](https://github.com/weka/weka-operator/commit/192ede1485edc880e02ab87ed0496014467c26da))
* conditions fixes ([756b9d9](https://github.com/weka/weka-operator/commit/756b9d9d435a5c5a4b9eb3303d37ca363fbe6714))
* hashsum for specific file in github action ([f342010](https://github.com/weka/weka-operator/commit/f342010e573c9de51cb731a6c369b0e192b5bc0a))
* nvme mode fix ([69e6242](https://github.com/weka/weka-operator/commit/69e6242a7818c23468c7f5aefdd1f213d3b9836a))
* packages refresh ([b26f164](https://github.com/weka/weka-operator/commit/b26f1642995a578103ed34df850b2a5bf76c32ee))
* properly discover ssd disks along with nvme ([0f6dc22](https://github.com/weka/weka-operator/commit/0f6dc22d73ed163ce81fb1e9b8cccbae2cf2f183))
* rbac in helm ([1c7b5a0](https://github.com/weka/weka-operator/commit/1c7b5a0dfbba989a6834bb1a1bbbe7513670608c))
* recursively unpack steps ([11a98ac](https://github.com/weka/weka-operator/commit/11a98ac4f0beec074f3bdd20c2d37cae466c86e1))
* releases re-do using github actions ([0e64d7e](https://github.com/weka/weka-operator/commit/0e64d7e69a96f04a4e45bf997af1d272ca066bc1))
* remove bad tests/leftovers of topology ([666a268](https://github.com/weka/weka-operator/commit/666a268b1d875f0d7b1f008536de9d38fc3f730e))
* remove mock-based testing, it is heavy and low value ([d0b0469](https://github.com/weka/weka-operator/commit/d0b0469ff4c255bbbfaa6078eb45ceea589cc1db))
* topology key set to hostname to make antiaffinity work ([2b237b9](https://github.com/weka/weka-operator/commit/2b237b9b155da5654967db8be15fe42780e41751))
* try go generate before running make generate ([97be2df](https://github.com/weka/weka-operator/commit/97be2dfce92f02e0591d8f46d62a32afc5b41689))

## [1.1.0-alpha.1](https://github.com/weka/weka-operator/compare/v1.0.0...v1.1.0-alpha.1) (2024-08-21)

### Features

* break out from topology ([91a5806](https://github.com/weka/weka-operator/commit/91a5806d412100eb01d7632e436bc000c78c1396))
* semrel releases ([4f14a8d](https://github.com/weka/weka-operator/commit/4f14a8d525556920ff7c7e6880e3351a609bf7a1))

### Bug Fixes

* packages refresh ([b26f164](https://github.com/weka/weka-operator/commit/b26f1642995a578103ed34df850b2a5bf76c32ee))

## [1.1.0-alpha.1](https://github.com/weka/weka-operator/compare/v1.0.0...v1.1.0-alpha.1) (2024-08-21)

### Features

* break out from topology ([91a5806](https://github.com/weka/weka-operator/commit/91a5806d412100eb01d7632e436bc000c78c1396))
* semrel releases ([4f14a8d](https://github.com/weka/weka-operator/commit/4f14a8d525556920ff7c7e6880e3351a609bf7a1))

### Bug Fixes

* packages refresh ([b26f164](https://github.com/weka/weka-operator/commit/b26f1642995a578103ed34df850b2a5bf76c32ee))
