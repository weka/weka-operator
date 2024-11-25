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
