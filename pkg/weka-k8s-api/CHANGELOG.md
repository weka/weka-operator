# [1.25.0](https://github.com/weka/weka-k8s-api/compare/v1.24.0...v1.25.0) (2025-04-17)


### Bug Fixes

* add an option to skip active mounts check before deleting client containers ([8241356](https://github.com/weka/weka-k8s-api/commit/8241356ec93cd7342947d80447c895255bbd3e46))
* add auto remove timeout for clients, default to 24h ([7ba80ad](https://github.com/weka/weka-k8s-api/commit/7ba80adbb41a9e94a4b033ee1a61bc634bbc99e9))
* add cluster overrides ([75d5180](https://github.com/weka/weka-k8s-api/commit/75d5180c02fafe8ee25556e95f7428a361523990))
* add deepcopy funcs ([0b100a2](https://github.com/weka/weka-k8s-api/commit/0b100a271c2290d869e113932cda4c1c00f97111))
* add descriptions ([62a64e1](https://github.com/weka/weka-k8s-api/commit/62a64e1d89b156f2d37356992191733330f77ef4))
* add lastappliedSpec to container, so cluster checks container ([426bd38](https://github.com/weka/weka-k8s-api/commit/426bd38a396462cd5983700465ac1d2c5c4be3d6))
* add machineIdentifier to resources ([81e6a35](https://github.com/weka/weka-k8s-api/commit/81e6a35610a83bb495be1634bb5affd872b2274e))
* add node affinity printer column on wekacontainer ([b7a4bb1](https://github.com/weka/weka-k8s-api/commit/b7a4bb1d38942483f28defb6c7ad93683826ca01))
* add umount on host override to force umount stuck mounts ([d325769](https://github.com/weka/weka-k8s-api/commit/d3257690af6167f25ad47843ac833818c58997ff))
* allow alternative drivers-loader for cluster ([91551d5](https://github.com/weka/weka-k8s-api/commit/91551d571ab29fcbeba16d8814c6815f4f524a81))
* allow for pre-start script on container, for custom setup before drivers build ([f0ecb68](https://github.com/weka/weka-k8s-api/commit/f0ecb686dad73d90c9ed196ef727b36c1fe1443c))
* allow pausing upgrade before compute phase ([b4cb675](https://github.com/weka/weka-k8s-api/commit/b4cb6752e1b62ffc645676539059ec3de6d5a839))
* allow pausing upgrade of cluster ([6e20855](https://github.com/weka/weka-k8s-api/commit/6e208559884789f485ef99769a734f98ed9ecfc0))
* an option not to evict compute on upgrade stop ([f6c4a7b](https://github.com/weka/weka-k8s-api/commit/f6c4a7b44d0d4bba339ebd615c2cfbfb642b603d))
* an option to start draining automatically on clients ([bcd95b4](https://github.com/weka/weka-k8s-api/commit/bcd95b44e76545f85731d3b31c0e890dd6324bb3))
* api enums fix ([114c321](https://github.com/weka/weka-k8s-api/commit/114c321714941e521c533a06b42975e1a551c03d))
* container exposePorts, to be exposed as pod ports ([bb49bf4](https://github.com/weka/weka-k8s-api/commit/bb49bf4bba2306938eddbdac89ecf9db486fc7f6))
* container-level api to set debug sleep timeout ([d5600a0](https://github.com/weka/weka-k8s-api/commit/d5600a061680ff8c64d7695a2b737574a78aa547))
* delete legacy driveOptions field on wekacontainer ([2e71549](https://github.com/weka/weka-k8s-api/commit/2e7154943e2bd39c090e4370359e1d2e5be75671))
* exposedPorts instead of exposePort as more advanced struct ([3eb4e8f](https://github.com/weka/weka-k8s-api/commit/3eb4e8f32c326b1c191c9685042f3420af660a6f))
* fix case of force resign payload ([f366347](https://github.com/weka/weka-k8s-api/commit/f3663476d9c5f0e384679fc3373dbb3505d7e840))
* hide pod resources config ([6473448](https://github.com/weka/weka-k8s-api/commit/647344826c16f6ccb6aa54a4e604a7c9b04bef4f))
* limit SignDrivesPayload to certain type values ([0ace35d](https://github.com/weka/weka-k8s-api/commit/0ace35dc826ee426f694b8c55a33dbf773b8dd9b))
* make manual op / policy image optional ([8eb5abf](https://github.com/weka/weka-k8s-api/commit/8eb5abf6b020e709e9b315cfd91f9b43d922b5a7))
* management ips plural variant. to be used for applicable network recognition ([6170062](https://github.com/weka/weka-k8s-api/commit/6170062112f4d3c3aa5041241faf772f9c3d01ea))
* metrics string accessors ([1360b70](https://github.com/weka/weka-k8s-api/commit/1360b70bc329f070a651eed687ef19e52a7c314e))
* metrics via endpoint ([e748f0d](https://github.com/weka/weka-k8s-api/commit/e748f0d349917e0541b95d461199ec90e1a7ac20))
* more cluster-wide metrics ([0f981ca](https://github.com/weka/weka-k8s-api/commit/0f981caedc96813b9ff4fb5cb244b73f6d9fbea8))
* move disregardRedundancy to overrides ([3bfce85](https://github.com/weka/weka-k8s-api/commit/3bfce85f65a76f6b6ef8b3705401ab7d2b6bd2af))
* move DriversLoaderImage to overrides ([dbd6cfb](https://github.com/weka/weka-k8s-api/commit/dbd6cfb7960f7e058eb487195feb6ff2b014f6d4))
* move ForceAio to overrides ([87a4b59](https://github.com/weka/weka-k8s-api/commit/87a4b59d31849fcd699b93bc92370fee478ed9b3))
* remove unused display field ([586d2b5](https://github.com/weka/weka-k8s-api/commit/586d2b54b36a28addb3f304a048db2cedf42d757))
* replace allow options with generic optinos ([50c7092](https://github.com/weka/weka-k8s-api/commit/50c709234b36652f4a1b74c8a566239fb760f62c))
* skipCleanupPersistentDir unsafe comment/explanation ([8fa7fa1](https://github.com/weka/weka-k8s-api/commit/8fa7fa1c3ba3a73d5316511fe5bbe994dfb8cab2))
* support csi endpoints subnets selector ([4082f40](https://github.com/weka/weka-k8s-api/commit/4082f401a88a58f7361c91198768c330facd0ed0))
* support for envoy additional memory ([6dcb5e0](https://github.com/weka/weka-k8s-api/commit/6dcb5e01bd24b7f58094dd2b2e9bbec266224211))
* support force upgrade of drives only ([6459776](https://github.com/weka/weka-k8s-api/commit/645977629f1593ace01604ee0bb06cadaa6acc65))
* support new sign utility flags ([67b14ee](https://github.com/weka/weka-k8s-api/commit/67b14ee2e12d6acdb2727ebe83fcb8e348412ae8))
* support shared resources client pod definition ([2a60e4e](https://github.com/weka/weka-k8s-api/commit/2a60e4e821e749c61e091ae1678011a422f82c82))
* take envoy into account when getting updatable additional memory ([e5eae7c](https://github.com/weka/weka-k8s-api/commit/e5eae7c089bd1aa568dabacae93a400b80ff9d04))
* types fix for autoremove timeout ([f969fa9](https://github.com/weka/weka-k8s-api/commit/f969fa9b7aee76e3d6d984f8917dc0269157f1ab))
* ugprade control for force replacements ([afd7ad8](https://github.com/weka/weka-k8s-api/commit/afd7ad863c017a6ba70153f771cd93132398b154))
* use duration type for wekapolicy interval ([69b406a](https://github.com/weka/weka-k8s-api/commit/69b406af81f06ee36c303a329e161888578873a6))


### Features

* add activeMounts to the wekacontainer stats ([f91af2c](https://github.com/weka/weka-k8s-api/commit/f91af2c6d4c0e8d34c1b4bae4fbe508ee2f01e24))
* add allowForceUpgrade field in wekacontainer spec ([2a3cf1b](https://github.com/weka/weka-k8s-api/commit/2a3cf1b4df83e3a238cad7184ce3e52a3e909907))
* add allowS3ClusterDestroy option to WekaCluster spec ([3725851](https://github.com/weka/weka-k8s-api/commit/3725851c775612993b1d403a65e7a3e997938d73))
* add destroying container state ([d221e8b](https://github.com/weka/weka-k8s-api/commit/d221e8b71a7442037a0ee3f2ad3a9d296165129f))
* add devicesSubnet field in network configuration ([2e352b2](https://github.com/weka/weka-k8s-api/commit/2e352b20d63380b0763646305c3eb03a1ba197a6))
* add draining wekacontainer status (for client container mode) ([64da5f2](https://github.com/weka/weka-k8s-api/commit/64da5f23e6b8b99a58956fcf8674c539cc828de6))
* add enum for wekaclient statuses ([abf87a8](https://github.com/weka/weka-k8s-api/commit/abf87a867c123676519cfbdd1db9888965b78a94))
* add explicit gateway param for clients and cluster ([4c755c6](https://github.com/weka/weka-k8s-api/commit/4c755c6df07d9f9ee386415bff839af9ac460f07))
* add GetHostIps, print management ips list ([d936da4](https://github.com/weka/weka-k8s-api/commit/d936da4d7c3056ad977f14fd28e8b8bc7f027fab))
* add internalStatus to wekacontainer status ([2e76265](https://github.com/weka/weka-k8s-api/commit/2e76265c087ce89df07f77d0030826d843f3b0c5))
* add leadershipRaftSize and bucketRaftSize ([4d30865](https://github.com/weka/weka-k8s-api/commit/4d30865e87c4745e70e76b79fea7c1509310215d))
* add managementIPsStr column for ips displaying ([f8ba78f](https://github.com/weka/weka-k8s-api/commit/f8ba78fcf5aeac82dd7561cd56b722f60df23580))
* add net devices to support ensure nics ([1290d22](https://github.com/weka/weka-k8s-api/commit/1290d22c14435905a8ca9645af0548f80d891f28))
* add NormalizeTolerations function ([eefaf1f](https://github.com/weka/weka-k8s-api/commit/eefaf1f9f401e08e592b58348837db3c1aed3842))
* add postFormClusterScript override, a script to run before startio ([9d0e08a](https://github.com/weka/weka-k8s-api/commit/9d0e08a07885fe169e3380b5f251eb20833892b8))
* add StartingIO and WaitForDrives cluster statuses ([9959774](https://github.com/weka/weka-k8s-api/commit/9959774b806e98af4007e6ae71eedd56c3d293a6))
* add startIoConditions for cluster, rm message from container ([da37d00](https://github.com/weka/weka-k8s-api/commit/da37d00c559e8e1391298c8b3880695edf517ad3))
* add type for wekacontainer instructions ([68578e7](https://github.com/weka/weka-k8s-api/commit/68578e7de26088b16618b35285ab66bbdc3fec4f))
* add unblock-drives manual operation action ([514ec4a](https://github.com/weka/weka-k8s-api/commit/514ec4a639ae7819a8c774384d8828ce4b2d199c))
* add wekacontainer deleting state ([75cd1bc](https://github.com/weka/weka-k8s-api/commit/75cd1bcdf8b3b722669ab9811b8a75fdaeadfeb9))
* add wekacontainer statuses, add validation for image ([6e553ce](https://github.com/weka/weka-k8s-api/commit/6e553ceae2db083c5c7e4e4d8b4a3c5e1b5cbef3))
* allowForceUpgrade -> allowHotUpgrade in wekacontainer spec ([3206343](https://github.com/weka/weka-k8s-api/commit/3206343a2531fee858471fc637cd694d3ca9a469))
* bring back tombstones removal ([88d015b](https://github.com/weka/weka-k8s-api/commit/88d015b283e165d9391b4ea703f43e95ce0aea60))
* clients counters on printer and status ([450d684](https://github.com/weka/weka-k8s-api/commit/450d6846e3cf0b978a4ed0b2714c26b9eb1e9b02))
* force upgrade flow ([8829e7e](https://github.com/weka/weka-k8s-api/commit/8829e7eade6986745e9baead8ad098e90e1e4490))
* generic CR timestamps for actions throttling ([2358f38](https://github.com/weka/weka-k8s-api/commit/2358f38a90b6d75c90d020ee38c0df356548b98d))
* machine identifier ref for clients ([a501851](https://github.com/weka/weka-k8s-api/commit/a5018517884151afde10310af34bd2af8af2ae37))
* make all-at-once default upgrade policy for client ([5a5ebd6](https://github.com/weka/weka-k8s-api/commit/5a5ebd69a7cd8d3f9d8c6a6233dac06abbc0ccfe))
* move container overrides to spec, remove cluster overrideGracefulDestroyDuration ([65ecf42](https://github.com/weka/weka-k8s-api/commit/65ecf429bf3b07d5553357d658cc8f9322908a0d))
* remote viewer tls config with trusted by default ([6afc28c](https://github.com/weka/weka-k8s-api/commit/6afc28c0810a8c87362cb2376c39f4c8d70a64cd))
* support multiple network devices ([508e697](https://github.com/weka/weka-k8s-api/commit/508e69726d4706edad099137d3caa200057e886d))
* update failure domain config for weka containers, allow compositeLabels ([c85cca2](https://github.com/weka/weka-k8s-api/commit/c85cca24993d1c5b1e9db30d95bf02f575fe2a7c))

# [1.23.0](https://github.com/weka/weka-k8s-api/compare/v1.22.0...v1.23.0) (2024-11-26)


### Bug Fixes

* add loader mode to IsAdhocOpContainer ([b78e5a5](https://github.com/weka/weka-k8s-api/commit/b78e5a5cc54ddf21f6fd45b375c3bd74ba52e182))
* change nfsGateway to nfs ([e5e5edc](https://github.com/weka/weka-k8s-api/commit/e5e5edce3c184249d9e46ac9dca1f0c0c4a2558f))


### Features

* manual operation of openning remote trace session ([f5f7c1f](https://github.com/weka/weka-k8s-api/commit/f5f7c1fca6eee781c1513bd1894104cfabdc4225))
* metrics via crd ([d9302bd](https://github.com/weka/weka-k8s-api/commit/d9302bd4bb6ff92def29057db043960b70c2fb3f))
* use crds as metrics storage ([a3ca575](https://github.com/weka/weka-k8s-api/commit/a3ca575db721bdb9c81bfdcdcf5283abb4b4c7bf))

# [1.22.0](https://github.com/weka/weka-k8s-api/compare/v1.21.0...v1.22.0) (2024-11-25)


### Bug Fixes

* add loader mode to IsAdhocOpContainer ([0df651e](https://github.com/weka/weka-k8s-api/commit/0df651e5f305d4766515ea3c7c09d3375e3b77b3))
* add loader mode to IsAdhocOpContainer ([#43](https://github.com/weka/weka-k8s-api/issues/43)) ([f33a857](https://github.com/weka/weka-k8s-api/commit/f33a857ed6ca8d8a859972d9c9896002ffeb730e))


### Features

* add ensure-nics manual operation ([c6fe92c](https://github.com/weka/weka-k8s-api/commit/c6fe92c407ca19b9a645add41de6ecc06c1db1f0))
* add ensure-nics manual operation ([#46](https://github.com/weka/weka-k8s-api/issues/46)) ([312c122](https://github.com/weka/weka-k8s-api/commit/312c122a82387b45fd74d943987b7486325ed7ea))
* add message field to weka container status ([3b015b0](https://github.com/weka/weka-k8s-api/commit/3b015b0d2ab08bb695bf5c0874d40cc3690a1401))

# [1.22.0](https://github.com/weka/weka-k8s-api/compare/v1.21.0...v1.22.0) (2024-11-25)


### Bug Fixes

* add loader mode to IsAdhocOpContainer ([0df651e](https://github.com/weka/weka-k8s-api/commit/0df651e5f305d4766515ea3c7c09d3375e3b77b3))
* add loader mode to IsAdhocOpContainer ([#43](https://github.com/weka/weka-k8s-api/issues/43)) ([f33a857](https://github.com/weka/weka-k8s-api/commit/f33a857ed6ca8d8a859972d9c9896002ffeb730e))


### Features

* add ensure-nics manual operation ([c6fe92c](https://github.com/weka/weka-k8s-api/commit/c6fe92c407ca19b9a645add41de6ecc06c1db1f0))
* add ensure-nics manual operation ([#46](https://github.com/weka/weka-k8s-api/issues/46)) ([312c122](https://github.com/weka/weka-k8s-api/commit/312c122a82387b45fd74d943987b7486325ed7ea))
* add message field to weka container status ([3b015b0](https://github.com/weka/weka-k8s-api/commit/3b015b0d2ab08bb695bf5c0874d40cc3690a1401))

# [1.21.0](https://github.com/weka/weka-k8s-api/compare/v1.20.0...v1.21.0) (2024-11-11)


### Features

* add stripe size and data protection level configs ([d9021af](https://github.com/weka/weka-k8s-api/commit/d9021afaf86f45e3fbb15ac78c741c4d0f4c7770))

# [1.20.0](https://github.com/weka/weka-k8s-api/compare/v1.19.0...v1.20.0) (2024-10-30)


### Features

* mark fields on container with p0 ([d416ed8](https://github.com/weka/weka-k8s-api/commit/d416ed85474587995f659bc97f3ffd70dc1ec8d1))

# [1.19.0](https://github.com/weka/weka-k8s-api/compare/v1.18.0...v1.19.0) (2024-10-24)


### Features

* add hugepages offset setting by container role to weka config ([0d7b64f](https://github.com/weka/weka-k8s-api/commit/0d7b64fc8a9e88d6211f4fc30a858e62d6991f48))

# [1.18.0](https://github.com/weka/weka-k8s-api/compare/v1.17.0...v1.18.0) (2024-10-22)


### Features

* add tolerations to tombstone spec ([94a2253](https://github.com/weka/weka-k8s-api/commit/94a22538d55c8326c9e9e052dec980f8a41f4bcf))

# [1.17.0](https://github.com/weka/weka-k8s-api/compare/v1.16.0...v1.17.0) (2024-10-22)


### Features

* add failureDomain to container allocations ([76d4527](https://github.com/weka/weka-k8s-api/commit/76d4527162697057058e49bb3487b88f522645f0))

# [1.16.0](https://github.com/weka/weka-k8s-api/compare/v1.15.0...v1.16.0) (2024-10-22)


### Features

* add forceAio setting for weka cluster (disabled by default) ([0bf9c4e](https://github.com/weka/weka-k8s-api/commit/0bf9c4ea94167c31f76b7407d2e9de31c9901f05))


### Reverts

* support iommuEnabled option for weka cluster ([9dcec57](https://github.com/weka/weka-k8s-api/commit/9dcec57f619bf5bbe863ce99989768e0bcb59c9b))

# [1.15.0](https://github.com/weka/weka-k8s-api/compare/v1.14.0...v1.15.0) (2024-10-21)


### Features

* support iommuEnabled option for weka cluster ([db01869](https://github.com/weka/weka-k8s-api/commit/db0186964838baa57481bf01268f59ebfb54707a))

# [1.14.0](https://github.com/weka/weka-k8s-api/compare/v1.13.0...v1.14.0) (2024-10-21)


### Features

* add PCI devices filtering option for signDrivesPayload ([4097a5f](https://github.com/weka/weka-k8s-api/commit/4097a5ffe86b1fc10c1782e10882fbd9b01f227b))

# [1.13.0](https://github.com/weka/weka-k8s-api/compare/v1.12.0...v1.13.0) (2024-10-21)


### Features

* support hotSpare setting for weka cluster (default 0) ([e4fac51](https://github.com/weka/weka-k8s-api/commit/e4fac512ee9ba66948801cd6ce567eb81a279c2e))

# [1.12.0](https://github.com/weka/weka-k8s-api/compare/v1.11.0...v1.12.0) (2024-10-21)


### Features

* add condition for JoinIpsSet ([d34d587](https://github.com/weka/weka-k8s-api/commit/d34d587d1aaf7bbcf7520d7a02c666d688cab5d8))

# [1.11.0](https://github.com/weka/weka-k8s-api/compare/v1.10.0...v1.11.0) (2024-10-20)


### Features

* add failure domain label and pod config (scheduling) for cluster ([70e01bc](https://github.com/weka/weka-k8s-api/commit/70e01bcd9b529f12646991aa4b91b22691577378))

# [1.10.0](https://github.com/weka/weka-k8s-api/compare/v1.9.0...v1.10.0) (2024-10-09)


### Features

* add labels on WekaContainerDetails ([8cc5340](https://github.com/weka/weka-k8s-api/commit/8cc5340d6f8d19f57a53e33344c17657a5ec1233))

# [1.9.0](https://github.com/weka/weka-k8s-api/compare/v1.8.0...v1.9.0) (2024-10-09)


### Features

* add GetForMode func on AdditionalMemory ([c049681](https://github.com/weka/weka-k8s-api/commit/c04968124a61296d51600ff080f2037ade720767))

# [1.8.0](https://github.com/weka/weka-k8s-api/compare/v1.7.0...v1.8.0) (2024-10-09)


### Features

* add portRange to wekaclient / wekacontainer spec ([bc5f508](https://github.com/weka/weka-k8s-api/commit/bc5f508fd06b54233863a2f58633dc21073d97a4))

# [1.7.0](https://github.com/weka/weka-k8s-api/compare/v1.6.0...v1.7.0) (2024-10-08)


### Features

* add gracefulDestroyTimeout field on WekaCluster ([69bb1f5](https://github.com/weka/weka-k8s-api/commit/69bb1f5408ff222c8b94d03acabeb4e415ceb0a6))

# [1.6.0](https://github.com/weka/weka-k8s-api/compare/v1.5.2...v1.6.0) (2024-10-08)


### Features

* add min length validation for tombstone CrId ([8d01ef9](https://github.com/weka/weka-k8s-api/commit/8d01ef99b1aba50fc8d0d98788704b0ee9125e77))

## [1.5.2](https://github.com/weka/weka-k8s-api/compare/v1.5.1...v1.5.2) (2024-10-07)


### Bug Fixes

* envoy weka container mode ([3616870](https://github.com/weka/weka-k8s-api/commit/3616870740082d9631985f5e663994cbe980ff05))

## [1.5.1](https://github.com/weka/weka-k8s-api/compare/v1.5.0...v1.5.1) (2024-10-07)


### Bug Fixes

* IsWekaContainer condition - include dist containers ([2bce35c](https://github.com/weka/weka-k8s-api/commit/2bce35cbc9f6c5e30e93f7c03b7b2f7480305d1b))

# [1.5.0](https://github.com/weka/weka-k8s-api/compare/v1.4.0...v1.5.0) (2024-10-03)


### Features

* support tolerations for WekaPolicy ([781457f](https://github.com/weka/weka-k8s-api/commit/781457f880bbc4e447aa9b267459001a8f9582c7))

# [1.4.0](https://github.com/weka/weka-k8s-api/compare/v1.3.0...v1.4.0) (2024-10-01)


### Features

* ability to specify custom image for drivers loader ([e09bdcb](https://github.com/weka/weka-k8s-api/commit/e09bdcb42ca0d3bf6dfdc8184e9f093d9cde778d))

# [1.3.0](https://github.com/weka/weka-k8s-api/compare/v1.2.2...v1.3.0) (2024-10-01)


### Features

* wekahome config api improvements ([f92633f](https://github.com/weka/weka-k8s-api/commit/f92633f10ace4ceba01d2346dfee84ec37459bed))

## [1.2.2](https://github.com/weka/weka-k8s-api/compare/v1.2.1...v1.2.2) (2024-09-30)


### Bug Fixes

* upgradePolicyType on container, to propagate desired behaviour ([9b60151](https://github.com/weka/weka-k8s-api/commit/9b601514f83115226f0dcf982a5653ba687ae97d))
* upgradePolicyType on container, to propagate desired behaviour ([#11](https://github.com/weka/weka-k8s-api/issues/11)) ([af75b24](https://github.com/weka/weka-k8s-api/commit/af75b24eb4f41584f1694f55ad6679e02da2a628))

## [1.2.1](https://github.com/weka/weka-k8s-api/compare/v1.2.0...v1.2.1) (2024-09-26)


### Bug Fixes

* drivers-dist in allowed values ([fd6ee7b](https://github.com/weka/weka-k8s-api/commit/fd6ee7b9ecd7f2c2f2b7e159bae3a9437f22bd39))

# [1.2.0](https://github.com/weka/weka-k8s-api/compare/v1.1.1...v1.2.0) (2024-09-26)


### Features

* policies should run only if pre-requisite policies have completed ([1a26f1c](https://github.com/weka/weka-k8s-api/commit/1a26f1c34d21a51cd9ae476376e2b227b7e89326))

## [1.1.1](https://github.com/weka/weka-k8s-api/compare/v1.1.0...v1.1.1) (2024-09-26)


### Bug Fixes

* rename new set of driver modes, keeping legacy intact ([5622d96](https://github.com/weka/weka-k8s-api/commit/5622d96a99ccfdf1a47893c1a498f5058cbd741d))

# [1.1.0](https://github.com/weka/weka-k8s-api/compare/v1.0.2...v1.1.0) (2024-09-26)


### Features

* upgrade policies for clients ([7c3371f](https://github.com/weka/weka-k8s-api/commit/7c3371f77f91fa86a24310f507b247c2b7d56a07))

## [1.0.2](https://github.com/weka/weka-k8s-api/compare/v1.0.1...v1.0.2) (2024-09-17)


### Bug Fixes

* IsDriversBuilder container mode condition ([959e2ee](https://github.com/weka/weka-k8s-api/commit/959e2eed8cb03235e0f08f7136d3b2ca1e89c6f9))

## [1.0.1](https://github.com/weka/weka-k8s-api/compare/v1.0.0...v1.0.1) (2024-09-16)


### Bug Fixes

* clean api - remove internal / unused logic ([63564c4](https://github.com/weka/weka-k8s-api/commit/63564c4e710e28364be0b485d7f33ac137585992))

# 1.0.0 (2024-09-15)


### Bug Fixes

* generate zz_ types from weka operator side ([5d0b9fd](https://github.com/weka/weka-k8s-api/commit/5d0b9fdea5f9f02ef8f8d95ae67928a7e6bed34f))


### Features

* new drivers flow integration ([1e98dd2](https://github.com/weka/weka-k8s-api/commit/1e98dd26dfb7fdfd5108d8cc053efe2b0ed45de9))
