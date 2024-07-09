# CHANGELOG

## v1.0.0-dev.1 (2024-07-09)

### Ci

* ci: build release ([`94775e0`](https://github.com/weka/weka-operator/commit/94775e0693c46339389f15cc4af924092b61076b))

* ci: dynamic release tags ([`22cb764`](https://github.com/weka/weka-operator/commit/22cb764ffb4eba2ad03dc04215256d5f0a18173e))

* ci: reusable setup ([`ac5e9fb`](https://github.com/weka/weka-operator/commit/ac5e9fb44b2e54d87298928ff815e4a9a06ad237))

### Feature

* feat: node tuning for OCP hugepages config (#194) ([`1e045f3`](https://github.com/weka/weka-operator/commit/1e045f37c0bd269d8a840701f11988367d0f9623))

* feat: add default nodeSelector for MachineConfigPool ([`0375a71`](https://github.com/weka/weka-operator/commit/0375a715451a6d7f378fa9ab2b2fab7bef70c9af))

* feat: add securityContextConstraints for OCP installation ([`ea5f016`](https://github.com/weka/weka-operator/commit/ea5f0162d431762d46c163e97c8cd887617ff320))

* feat: node tuning for OCP hugepages config ([`83d9060`](https://github.com/weka/weka-operator/commit/83d9060703aec45e4410a899656262a5d71c99be))

* feat: envoy-specific build trigger ([`97fbb08`](https://github.com/weka/weka-operator/commit/97fbb08bf59a4b4a8da3cab68bc6ef02dc3ccbd6))

### Fix

* fix: remove semrelrc ([`c32ba72`](https://github.com/weka/weka-operator/commit/c32ba72c89d7c1fd679794d59db85efbcab2b5b4))

### Test

* test: add arg to test-functional

Specify `RUN` argument to `make test-functional` to select the test
case. ([`1e266d8`](https://github.com/weka/weka-operator/commit/1e266d8039cf536d98091be15f43b838a9d76063))

### Unknown

* Merge pull request #174 from weka/05-24-test_parameterize_functional_test

test: parameterize functional test target ([`326e3dd`](https://github.com/weka/weka-operator/commit/326e3dd1f6f8b3600b14b02758da8ac9fe46b39f))

* Merge remote-tracking branch &#39;origin/release/v1-beta&#39; ([`9036b2e`](https://github.com/weka/weka-operator/commit/9036b2e044252d02bf75418c48c47467999ebbbb))

## v1.0.0-beta.32 (2024-07-05)

### Fix

* fix: tombstone create should ensure pod deletion after create ([`56002bc`](https://github.com/weka/weka-operator/commit/56002bc2714fa63f9654d36f54d894a854e0181c))

### Unknown

* Merge branch &#39;release/v1-beta&#39; ([`33330bc`](https://github.com/weka/weka-operator/commit/33330bc4a3ffbd5248c911071ccc56535cf050d8))

## v1.0.0-beta.31 (2024-07-04)

### Fix

* fix: schema fix, include forceAllowDriveSign for container

used by custom topologies to auto-sign drives ([`424db82`](https://github.com/weka/weka-operator/commit/424db82e61c47d9cda48115226bdca22d26544cc))

## v1.0.0-beta.30 (2024-07-03)

### Fix

* fix: do not restart client pods on image update ([`91f8535`](https://github.com/weka/weka-operator/commit/91f85353e308cfa1654aebbf2ffb573e3b089d32))

* fix: duplicate topology record ([`f4f540b`](https://github.com/weka/weka-operator/commit/f4f540b163c20d3911dacf144410deb9ae61f115))

### Unknown

* Merge branch &#39;release/v1-beta&#39; ([`468e98f`](https://github.com/weka/weka-operator/commit/468e98f045dea28c46101ff492b8abeb2a459c75))

## v1.0.0-beta.29 (2024-07-02)

### Fix

* fix: configure_traces() not working due to missing freeze_period ([`17356a1`](https://github.com/weka/weka-operator/commit/17356a1746c3b56e0145de86deb52a68f0fdf991))

## v1.0.0-beta.28 (2024-07-02)

### Fix

* fix: respect forceDriveSign on topology level ([`533934b`](https://github.com/weka/weka-operator/commit/533934b0afcd56865b76a2b0781eb0dbc2c88e8b))

## v1.0.0-beta.27 (2024-07-02)

### Chore

* chore: example memory reductions ([`06a818d`](https://github.com/weka/weka-operator/commit/06a818d5ebd9ceef4cd2996171370e9686fc1ab4))

### Fix

* fix: cluster reconcile should filter for backend containers only ([`8e02dce`](https://github.com/weka/weka-operator/commit/8e02dce8f58391e06d6f45566e7567b9233cc8cf))

## v1.0.0-beta.26 (2024-06-30)

### Chore

* chore: new line in the end of values.yaml ([`832d43d`](https://github.com/weka/weka-operator/commit/832d43dd4f9b872e9229ea70fcf0f010e10aeb93))

### Feature

* feat: propagate updates to WekaContainer from higher level

- imagePullSecret
- tolerations
- additionalMemory
- distService
- image(clients only), on backends it is more complex flow, that also
  in-place ([`77684df`](https://github.com/weka/weka-operator/commit/77684dfebf2c87ca967a0c0722377a1ca22f52e1))

* feat: an option to add additional memory in Mi to pods ([`547d8a4`](https://github.com/weka/weka-operator/commit/547d8a424b4d09bd3fdd9fee9caf09193e8445e8))

### Fix

* fix: dist service should not be doing -g stop ([`7dabd7f`](https://github.com/weka/weka-operator/commit/7dabd7f718b9ab82be9b540fb0d5d1b182f979a3))

* fix: clients should be stopped on pod delete ([`7e2d6b3`](https://github.com/weka/weka-operator/commit/7e2d6b38f4a3e81a89a1fd625740e76d0603b45f))

* fix: populate tolerations to driver loader and discovery ([`b15118e`](https://github.com/weka/weka-operator/commit/b15118e52eb614e4acb69d77f99cbd29d839d35c))

## v1.0.0-beta.25 (2024-06-30)

### Feature

* feat: add i3en3x topology ([`e8484e3`](https://github.com/weka/weka-operator/commit/e8484e32141e308add63d73bb0079dabf72171f3))

### Fix

* fix: configure_traces() not working due to missing freeze_period (#189) ([`7da5507`](https://github.com/weka/weka-operator/commit/7da55079569ba4eeb756f8eb997b49cdc4b62bd9))

* fix: missing trailing new line in values.yaml ([`111fc13`](https://github.com/weka/weka-operator/commit/111fc13a453e823fee57d98e1c6eba6f0ebbb965))

* fix: configure_traces() not working due to missing freeze_period ([`85a4e97`](https://github.com/weka/weka-operator/commit/85a4e978e2ea3e944557c4d59b03565e3606a202))

### Test

* test: update functionals to use bliss ([`b40fab5`](https://github.com/weka/weka-operator/commit/b40fab5df14c11a975b7d0b127a9e5e37b979ce5))

### Unknown

* Merge pull request #186 from weka/mbp/bliss-functionals

test: update functionals to use bliss ([`28250c8`](https://github.com/weka/weka-operator/commit/28250c8bd6af919b842fb314e140ec2fcb7d22d8))

* Merge branch &#39;release/v1-beta&#39; ([`07f25c7`](https://github.com/weka/weka-operator/commit/07f25c7f2249f25a09d8a00160f3e3ffeb52e485))

## v1.0.0-beta.24 (2024-06-27)

### Feature

* feat: container-lock via unix domain socket

this allows to handle forceful termination of pods
new pod will write generation
old pod will recognize new generation and suicide with --force
only then new one(after few crashes on socket bind) will star working,
avoiding races ([`462506f`](https://github.com/weka/weka-operator/commit/462506f57f62837d6b6bfacd5f89a37f8aa62a06))

## v1.0.0-beta.23 (2024-06-26)

### Feature

* feat: rely on conditional mounts to propagate /etc/resolv.conf ([`5604b84`](https://github.com/weka/weka-operator/commit/5604b847a250358d7b80ba678a20e3ff125ffb0e))

## v1.0.0-beta.22 (2024-06-26)

### Fix

* fix: memory increase to allow running 4.3 ([`0bbb9ec`](https://github.com/weka/weka-operator/commit/0bbb9ec6c5d200e0c07d4ba4a83fa543a43a7214))

## v1.0.0-beta.21 (2024-06-26)

### Chore

* chore: hp 2drives templates aligments ([`7074eff`](https://github.com/weka/weka-operator/commit/7074effaa71f896145b12aa27c59d846a71539b4))

* chore: hp2 clients example fixed target cluster ([`f250216`](https://github.com/weka/weka-operator/commit/f2502168c4ffc9dfeced4a3ef463c308c07432b2))

### Fix

* fix: add more memory to containers, physical setup requires more ([`c5a89a4`](https://github.com/weka/weka-operator/commit/c5a89a40a1243dadfff0b28b275ca0c9c0187221))

## v1.0.0-beta.20 (2024-06-26)

### Chore

* chore: hp_2drives topology example ([`2d65edc`](https://github.com/weka/weka-operator/commit/2d65edc30d62b2d09eb512508a4b9f6f92f19015))

### Fix

* fix: skip dependencies on driver build ([`030ad03`](https://github.com/weka/weka-operator/commit/030ad036890c0deff76618e921db6835a12efd49))

## v1.0.0-beta.19 (2024-06-26)

### Fix

* fix: remove nodeInfo from physical hp topology ([`9eac68c`](https://github.com/weka/weka-operator/commit/9eac68cfde056f500d24a657afd6cee692558f50))

## v1.0.0-beta.18 (2024-06-26)

### Feature

* feat: hp-based(hacky) topology to run on physical hp servers ([`c4e1142`](https://github.com/weka/weka-operator/commit/c4e11423868d575d85e6c6affff5832a779100b1))

## v1.0.0-beta.17 (2024-06-26)

### Fix

* fix: remove not needed  set of tls strictness ([`01d2a99`](https://github.com/weka/weka-operator/commit/01d2a999832c99fe7086684409d9eb7e0a93f91f))

## v1.0.0-beta.16 (2024-06-25)

### Fix

* fix: rely on pre-defined external-mounts param in config ([`c4d2307`](https://github.com/weka/weka-operator/commit/c4d2307b247ed6f326f71f6d4ac60fcb133c0628))

## v1.0.0-beta.15 (2024-06-25)

### Feature

* feat: maintain(no cleanup yet) persistent cleanup directories ([`025d9a1`](https://github.com/weka/weka-operator/commit/025d9a16b20d18489a9c743027d35ae760d52ed0))

### Fix

* fix: correct string for https enforcement ([`158f286`](https://github.com/weka/weka-operator/commit/158f286d0a2452da2aa5e65b9a785fa7a70f14af))

## v1.0.0-beta.14 (2024-06-25)

### Feature

* feat: enforce TLS on cluster ([`7d517cd`](https://github.com/weka/weka-operator/commit/7d517cd9e1ae352f896689a81ffa85fda327acf6))

* feat: create CSI secret with https scheme ([`fcaa48c`](https://github.com/weka/weka-operator/commit/fcaa48c5ee5a38ad7c85dacb05cd61ca5400f557))

## v1.0.0-beta.13 (2024-06-23)

### Fix

* fix: daemon-stop: expect and ignore cancellation error ([`37648a3`](https://github.com/weka/weka-operator/commit/37648a3f73b2b5408f7fae3b40f5bd0356e5f58c))

## v1.0.0-beta.12 (2024-06-23)

### Build

* build: fix typo in clean target ([`9722d92`](https://github.com/weka/weka-operator/commit/9722d9217608cbcee1c64d3d47c4a2783f690352))

### Feature

* feat: upgrade with phases ([`29e6750`](https://github.com/weka/weka-operator/commit/29e6750f6f5066081a35180a251b9b7693b0478d))

* feat: add i3en3x topology ([`8311d53`](https://github.com/weka/weka-operator/commit/8311d539db92e9c763c0dcc099d1ba35ac00a3bc))

* feat: add us-east-2, us-west-1 regions, and update account cloud-dev 303605160296 ([`8a1dad1`](https://github.com/weka/weka-operator/commit/8a1dad1b08a85aae850257d932dd1bb8fdf843d4))

* feat: upgrade with phases ([`d03b22e`](https://github.com/weka/weka-operator/commit/d03b22e23bfd340fad6dd3fba3955a5e51cda802))

### Fix

* fix: reset drive condition on pod recreate ([`78f75d4`](https://github.com/weka/weka-operator/commit/78f75d4e44f224503b25ff9ca981b5504bdc0373))

* fix: maintain always-running daemons, to recover from crashes ([`d746cba`](https://github.com/weka/weka-operator/commit/d746cba78b4752c38f378d98195be27cfbe126f5))

* fix: daemon-stop: expect and ignore cancellation error ([`ed4e332`](https://github.com/weka/weka-operator/commit/ed4e3329bc685ad13370c5e26b06f84114223441))

* fix: reset drive condition on pod recreate ([`1c53b89`](https://github.com/weka/weka-operator/commit/1c53b897088a0d7ad152fa3e291c1f3ad886edf5))

* fix: maintain always-running daemons, to recover from crashes ([`9a56154`](https://github.com/weka/weka-operator/commit/9a561547fbbad8de7af896cac6de6ff40a812ae8))

### Unknown

* Merge pull request #184 from weka/mbp/typo-clean-target

build: fix typo in clean target ([`57685e6`](https://github.com/weka/weka-operator/commit/57685e657c54e87e14e380cdb0d7c84e1da5e03f))

* Merge remote-tracking branch &#39;origin/release/v1-beta&#39; ([`6cc3a66`](https://github.com/weka/weka-operator/commit/6cc3a661cd8857956d0198b6d9a7adbec66f5907))

## v1.0.0-beta.11 (2024-06-20)

### Fix

* fix: allow 2 containers on bliss 6x instances ([`fb0b8d1`](https://github.com/weka/weka-operator/commit/fb0b8d1059e823cfaa0fc32f84f460fdd2d441cd))

## v1.0.0-beta.10 (2024-06-19)

### Feature

* feat: ipv6 udp ([`835eb8e`](https://github.com/weka/weka-operator/commit/835eb8e49aad760aae2c0fa43154e93db5f380a2))

## v1.0.0-beta.9 (2024-06-16)

### Feature

* feat: auto configure weka home ([`8d14187`](https://github.com/weka/weka-operator/commit/8d14187a6d00bd8293f5d565f75e117a4d074e27))

## v1.0.0-beta.8 (2024-06-13)

### Feature

* feat: re-integrate with driver builder drivers skip ([`bee1e5d`](https://github.com/weka/weka-operator/commit/bee1e5d5d75af54c5e278f653fbef26a65c53864))

## v1.0.0-beta.7 (2024-06-13)

### Fix

* fix: access version info only via version_params and not map directly

due to more dynamic manipulations post map create ([`31782ce`](https://github.com/weka/weka-operator/commit/31782ce9df31a41df755b63a9ab6c1a44cc9b3ae))

* fix: access version info only via version_params and not map directly

due to more dynamic manipulations post map create ([`d40abbc`](https://github.com/weka/weka-operator/commit/d40abbcd94b9a44df67942bf0b4b277df1a175a4))

### Unknown

* Merge branch &#39;release/v1-beta&#39; ([`a94532c`](https://github.com/weka/weka-operator/commit/a94532cd4955c87902ffa1d1c1b9a3be00583821))

## v1.0.0-beta.6 (2024-06-13)

### Feature

* feat: configurable by helm value/env var for controller debug sleep ([`8018dce`](https://github.com/weka/weka-operator/commit/8018dce6d748ecce845cea87971bbfc0a550eed2))

## v1.0.0-beta.5 (2024-06-13)

### Build

* build: prefix all version tags with &#39;v&#39; ([`b48ffa5`](https://github.com/weka/weka-operator/commit/b48ffa50e989e2d0c64b9bc20e5fa7b4063fddf0))

### Fix

* fix: implicit support for all 64.multitenancy versions ([`1ed19ab`](https://github.com/weka/weka-operator/commit/1ed19abaa2736f7523a93aa7bd70de93ce24f4dd))

## v1.0.0-beta.4 (2024-06-10)

### Fix

* fix: remove flaky test ([`32768a4`](https://github.com/weka/weka-operator/commit/32768a401eba2adb504869e18da142c770c5e160))

## v1.0.0-beta.2 (2024-06-10)

### Chore

* chore: oci bless template support ([`0fc8c11`](https://github.com/weka/weka-operator/commit/0fc8c11bcca7c574d32e74aeda03527d9f0cb02d))

### Ci

* ci: remove initial-development-release flag ([`461737a`](https://github.com/weka/weka-operator/commit/461737a694ca6414bb3bd5f9fb64c57f5cb7a50d))

### Fix

* fix: trigger build ([`94a0a0a`](https://github.com/weka/weka-operator/commit/94a0a0adaf1627fed5974cdc7d31adc4a5c8afa7))

### Test

* test: disable failing test ([`1fa8693`](https://github.com/weka/weka-operator/commit/1fa869338224b3d5121684e84f92fde70fdbf4d5))

## v1.0.0-beta.1 (2024-06-10)

### Ci

* ci: beta release branch ([`88ebc34`](https://github.com/weka/weka-operator/commit/88ebc340529f4fc880fbefc037ab8faf2daa208b))

### Fix

* fix: drivers: new weka driver command initial integration

this removes the need to hard coded weka versions
with this initial commmit has requirement on * Anton Bykov : b37e7952a71  (HEAD -&gt; weka-as-container-new-drivers, origin/weka-as-container-new-drivers) on wekapp side

more work needed on wekapp side to integrate this into dev
various ifs around `weka_drivers_handling` allow to keep it
back-compatible with older versions ([`23225f2`](https://github.com/weka/weka-operator/commit/23225f2d9d3297dec2716adefbf4462b367dbcfa))

## v0.63.0 (2024-06-02)

### Chore

* chore: remove noisy log ([`3fa35fa`](https://github.com/weka/weka-operator/commit/3fa35faaaf5cbca7a3b6c9f5e1065b16ed09efed))

* chore: changes to accomodate deployments by bless ([`ee757ac`](https://github.com/weka/weka-operator/commit/ee757acc498d5f169fe0abf4e44990bb3b8f8260))

### Fix

* fix: waiting short period of time for agent to boot, then restart ([`731d83c`](https://github.com/weka/weka-operator/commit/731d83cdc3aa03fc993564c492679f74884e57f8))

* fix: when using weka version 4.2 do not stop using -g ([`3aba3ae`](https://github.com/weka/weka-operator/commit/3aba3ae78d463bc971a78d5bb5f6b660d4ebf258))

### Unknown

* Merge remote-tracking branch &#39;origin/main&#39; into release/v0 ([`7935167`](https://github.com/weka/weka-operator/commit/7935167bc842a41fd461257ab65ca6826c3edb78))

## v0.62.0 (2024-05-28)

### Unknown

* Merge remote-tracking branch &#39;origin/main&#39; into release/v0 ([`435d5d6`](https://github.com/weka/weka-operator/commit/435d5d64ac7f1d9215f6eccff1188def6020162a))

## v0.61.1 (2024-05-22)

### Fix

* fix: implicitly support upcoming multitenancy. versions ([`d1073c1`](https://github.com/weka/weka-operator/commit/d1073c1105b0a478485f2b8ffc14870b69a55225))

## v0.61.0 (2024-05-22)

### Build

* build: prefix all version tags with &#39;v&#39; ([`9203fdc`](https://github.com/weka/weka-operator/commit/9203fdc4b49d8a1a9f536e5aafc90981668cfa5c))

### Chore

* chore: removed hack-athon template ([`518802d`](https://github.com/weka/weka-operator/commit/518802de10d0bf6190f98104ba96405f3a538111))

* chore: remove noisy log ([`f4e10f9`](https://github.com/weka/weka-operator/commit/f4e10f92d64d04d6494082a65066bea3bdc554e7))

* chore: new images with larger ebs ([`f3406cc`](https://github.com/weka/weka-operator/commit/f3406cc11a8b1e5d728c4d4245ad9f5bba41cdfd))

* chore: move to 4.3 as a default ([`4b08e56`](https://github.com/weka/weka-operator/commit/4b08e56efac8f12ad5d818669e97feeeca3f65ee))

### Ci

* ci: configuration for release branch (#163)

### TL;DR

This PR updates package.yaml and README.md, introducing several changes related to project releases.

### What changed?

In package.yaml, the branch triggering the workflow has been updated to include `release/v0`.  The new release branch `05-20-ci_dev-release_branch` has been added temporarily for testing purposes only. The Go version setup is enclosed in double quotes for consistent yaml style.

Instructions for creating a new release are updated to reflect the merge changes to the `release/v0` branch rather than the `main` branch, and instructions for development releases have been added. Minor markdown formatting changes are introduced for cleaner code blocks.

### How to test?

To test these changes, follow the new release instructions in the README.md and check if the workflow is triggered when a push to the mentioned branches is made.

### Why make this change?

These changes ensure that the project&#39;s release process is clearly defined and that the formatting within the README.md is neat and follows consistency.

--- ([`b7a0a84`](https://github.com/weka/weka-operator/commit/b7a0a844ab5f7067873f414555e6136705f9327e))

### Documentation

* docs: how to build pytest env ([`4efe5e0`](https://github.com/weka/weka-operator/commit/4efe5e0b1c2ad231715d362e2825f78dbcb30bf0))

* docs: docs for e2e tests ([`0c60baa`](https://github.com/weka/weka-operator/commit/0c60baa7a6fc2cca4478be5b3e254d24bc8466ad))

### Feature

* feat: release test ([`ebe1eee`](https://github.com/weka/weka-operator/commit/ebe1eee985b6f4466de77903d04f6f1e46e62e56))

* feat: share image with internal accounts ([`5f40172`](https://github.com/weka/weka-operator/commit/5f4017240c9737100927554d05a84ba97f410bd0))

* feat: create default FS ([`44954f9`](https://github.com/weka/weka-operator/commit/44954f95654be6da2ec9136a5069fc21a665a324))

* feat: create example/converged secret for CSI ([`83fe884`](https://github.com/weka/weka-operator/commit/83fe8848eab29908ab544273b22dd48258c4404b))

* feat(aws): dpdk support (not EKS) ([`8a4440a`](https://github.com/weka/weka-operator/commit/8a4440ae22e2f984167ec19e0da35a5f878b0f35))

* feat: upgrade support ([`bfd85a3`](https://github.com/weka/weka-operator/commit/bfd85a35e1298e372096c0ddbccc76d1d527975a))

* feat: moving to s3(dev version for now) as a default ([`04de532`](https://github.com/weka/weka-operator/commit/04de532baebe4c9735de891121266561c60bda76))

* feat: set envoy/s3 container names ([`0596249`](https://github.com/weka/weka-operator/commit/05962499d51bfc1ca480b683bab1ae3c73fd5b80))

* feat: tolerations support on wekaClient and wekaCluster levelt ([`019e1f5`](https://github.com/weka/weka-operator/commit/019e1f53ffc73fe1a33a12d1ec9ef6e77bcaf49d))

### Fix

* fix: return wrongly disabled metrics ([`5eea745`](https://github.com/weka/weka-operator/commit/5eea7451d0158f57e0d159d95a6a06a3e57014d8))

* fix: weka verison bump to make s3 work on 4.3 ([`bf7b863`](https://github.com/weka/weka-operator/commit/bf7b863b8b7dde623d16a906c714315c324627f9))

* fix: retry loading drivers in-process, bump to s3-fixed version ([`250b8f1`](https://github.com/weka/weka-operator/commit/250b8f1ab0f029dda354ab5874d145a029348025))

* fix: requeue if driver builder not available ([`f6a2e9e`](https://github.com/weka/weka-operator/commit/f6a2e9e76650781071ac9758fdaaca32e2a58152))

* fix: finalizers fix and removal of noisy logs ([`b467651`](https://github.com/weka/weka-operator/commit/b46765114a959b799b9c65d34806c3cc70083c83))

* fix: implicitly support upcoming multitenancy. versions ([`6cc98fd`](https://github.com/weka/weka-operator/commit/6cc98fd29801c8a0f1d766be5b262aff8b0de458))

* fix: configuration for release branch ([`84b8513`](https://github.com/weka/weka-operator/commit/84b8513c9831c8c13cc076a4d01b3e6df4c30216))

### Unknown

* Merge pull request #166 from weka/main

Create Release ([`4d79b31`](https://github.com/weka/weka-operator/commit/4d79b3135c32d98d314ccfbb1237824445178914))

* doc: docs for e2e tests (#164)

This PR updates the README file by adding more detailed instructions and explanations for a Python-based pytest suite, and modifying the prerequisites for running the E2E test suite. 

--- ([`4d56395`](https://github.com/weka/weka-operator/commit/4d563957f3e6a9869de3460a9c1ab5ad32278f9f))

## v0.60.0 (2024-05-21)

### Build

* build: go generate before tidy ([`6af8aa9`](https://github.com/weka/weka-operator/commit/6af8aa968f25b2225cfb26377df46c631dd44dc7))

### Chore

* chore(packer): add manifest that contains previous run output ([`e8d8339`](https://github.com/weka/weka-operator/commit/e8d8339b6a43bd9d7be898a8aeb79a3eb897e3b2))

* chore: merge fixes ([`81ebcc4`](https://github.com/weka/weka-operator/commit/81ebcc4a44f859e6b8105038290b028661552cd5))

* chore: expand oci template/regenerate ([`8762913`](https://github.com/weka/weka-operator/commit/876291382e3b96817b804f231a7a78249e50630f))

* chore: s3-compatible, expecting 8 cores system ([`0773e61`](https://github.com/weka/weka-operator/commit/0773e61b2bea678b4fde9aae426e5683c6ec86ef))

* chore: align on s3 multitenancy version ([`24db486`](https://github.com/weka/weka-operator/commit/24db486519c70529913e220b1770bd6603c29094))

### Feature

* feat: secret join and token support ([`29d0b71`](https://github.com/weka/weka-operator/commit/29d0b71df659b26d6fc5c7df6ea996c5edb2e805))

### Refactor

* refactor: ensure weka containers ([`79aabe9`](https://github.com/weka/weka-operator/commit/79aabe965e690c49f47e7596b4968405821206e6))

* refactor: discovery service ([`99269de`](https://github.com/weka/weka-operator/commit/99269de388126818fcde4de954b84222caaaccd2))

* refactor: new weka container for weka cluster ([`03a4e35`](https://github.com/weka/weka-operator/commit/03a4e3542d1d40b6bf9614906a342e993c5a35f9))

* refactor: update allocations configmap ([`cf2cb6e`](https://github.com/weka/weka-operator/commit/cf2cb6e8b016fce4b8120de998a940cfa694f1d7))

* refactor: ensure login credentials ([`e566dff`](https://github.com/weka/weka-operator/commit/e566dff57b8d94148300140747e90c269781fd15))

* refactor: getorinitallocmap ([`38e2cea`](https://github.com/weka/weka-operator/commit/38e2cea82eb8fc4a65ac4d6534743764f11a006d))

* refactor: move allocator to domain ([`5ffbf1f`](https://github.com/weka/weka-operator/commit/5ffbf1fbf8a3b30d48baad1fef68cac37d49b847))

* refactor: service for create cluster ([`0f5ea58`](https://github.com/weka/weka-operator/commit/0f5ea588cdb213d9e7a095c130d4a5bc052296d4))

* refactor: exec service ([`dcab603`](https://github.com/weka/weka-operator/commit/dcab603e35e695a086aa7a3ae51d4d414cf594f3))

### Unknown

* Merge pull request #156 from weka/05-17-refactor_update_allocations_configmap

refactor: update allocations configmap ([`ae8ec00`](https://github.com/weka/weka-operator/commit/ae8ec007d324387c1b1ee0e71d0569b39b56ffe1))

* Merge pull request #160 from weka/05-19-chore_align_on_s3_multitenancy_version

build: go generate before tidy ([`83fd973`](https://github.com/weka/weka-operator/commit/83fd973737e46f42a257e11b3d4204657d74f375))

## v0.59.1 (2024-05-15)

### Fix

* fix: can&#39;t have active container before cluster form ([`c7814d8`](https://github.com/weka/weka-operator/commit/c7814d8f43e272941eea570161622dc08c6b4ca9))

## v0.59.0 (2024-05-15)

### Feature

* feat: s3 cluster healing ([`c3c3104`](https://github.com/weka/weka-operator/commit/c3c31049775769c43f0ecc5bad1bb9f72f0ec53f))

## v0.58.0 (2024-05-15)

### Feature

* feat: rudimentary autohealing for backends ([`71a153b`](https://github.com/weka/weka-operator/commit/71a153bab25f78dddf40b9a16ae510777ee6ea17))

## v0.57.0 (2024-05-15)

### Feature

* feat: add tolerations and container-level stub ([`9c93fb5`](https://github.com/weka/weka-operator/commit/9c93fb530fdabee4100ab63e0e6e64b804b5323f))

### Test

* test: python/yaml e2e tests (#140)

An end-to-end test suite in Python/pytest that uses manifest YAMLs.

See `test/e2e/README.md` ([`527edbc`](https://github.com/weka/weka-operator/commit/527edbc4d5ab1b097ff061fe6b6f1bac26e6ee5a))

* test: python/yaml e2e tests ([`9628b7e`](https://github.com/weka/weka-operator/commit/9628b7e9ba51b927811fb4a79d844baefdf9872e))

* test: e2e tests on oci cluster (#134)

This PR adds a basic functional test suite.  It is indented for developer use.

See `test/functional/README.md`. ([`97bf354`](https://github.com/weka/weka-operator/commit/97bf354dfd465b6d3fbff0ed47160378faff9e96))

* test: e2e tests on oci cluster ([`34ea5a0`](https://github.com/weka/weka-operator/commit/34ea5a0b0c88f7bbd9debbd383f45039070c5b0b))

## v0.56.0 (2024-05-13)

### Feature

* feat: support of client rotation ([`ec9865e`](https://github.com/weka/weka-operator/commit/ec9865eba585210705a1aeb688841e37db9bc470))

## v0.55.0 (2024-05-12)

### Feature

* feat: s3 cluster support, weka service, autocreate fs ([`f1ddc53`](https://github.com/weka/weka-operator/commit/f1ddc534c2134a5a60da8863d9fe600a2d15729b))

## v0.54.1 (2024-05-09)

### Chore

* chore: templates cleanup, just small and large for now ([`aae730f`](https://github.com/weka/weka-operator/commit/aae730fb01fe0a42736eea6334cd8189e5114a53))

### Fix

* fix: builder container shutdown sequence, un-stuck buggy async ios ([`ce2f6ef`](https://github.com/weka/weka-operator/commit/ce2f6ef1b9f4a99dc79acdcdf6aa380a6394abd7))

## v0.54.0 (2024-05-09)

### Feature

* feat: persistence directories deletion via tombstone ([`a79c978`](https://github.com/weka/weka-operator/commit/a79c978184dd63087d1105150f716452b30356d3))

### Fix

* fix: better shared cpu numbers and oci setup demo ([`0da342e`](https://github.com/weka/weka-operator/commit/0da342ecdce769f4e3a8b03742a91e2f4b3d2e68))

## v0.53.7 (2024-05-08)

### Fix

* fix: override dependencies marker in dist ([`af8b695`](https://github.com/weka/weka-operator/commit/af8b695a44ec61c50a5d7b17f2e18e63e11095ab))

## v0.53.6 (2024-05-05)

### Chore

* chore: use remote signoz endpoint by default

chore: use remote signoz endpoint by default

chore: signoz: use rnd deployment as default ([`f9524c4`](https://github.com/weka/weka-operator/commit/f9524c4cb16b08675db3decc41e6228a95d48216))

### Fix

* fix: memory allocations aligment to  be in range of 90-93% utilisation ([`aae617e`](https://github.com/weka/weka-operator/commit/aae617ed515c0e96661d2317f929abf135728c2c))

## v0.53.5 (2024-04-28)

### Fix

* fix: logrotate: explicitly specify sizes and limit to 1x3 mib ([`984d7c5`](https://github.com/weka/weka-operator/commit/984d7c550d896b494c69fba6809cd319bc35587f))

* fix: set ephemeral-storage request on containers ([`c0629fe`](https://github.com/weka/weka-operator/commit/c0629fe98c7fbe6d4b30f2eb79e7edced5d1a796))

## v0.53.4 (2024-04-25)

### Fix

* fix: driver builder: fix weka driver reference ([`bd50a9b`](https://github.com/weka/weka-operator/commit/bd50a9b7c86251c760445db5ad72704aefd5e224))

## v0.53.3 (2024-04-25)

### Fix

* fix: driver-loader mapping was off for 4.2.10 ga ([`a79b87d`](https://github.com/weka/weka-operator/commit/a79b87d5c6a39b8a2db1f739b42cb38915662cf9))

## v0.53.2 (2024-04-25)

### Chore

* chore: directly align --version with tag, leaving an option for override ([`5a6f30d`](https://github.com/weka/weka-operator/commit/5a6f30de61f181d6fe03ffa3903eb61f690419b0))

* chore: ensure backends container trace span name fix ([`060c902`](https://github.com/weka/weka-operator/commit/060c9023bfd2f2705e1e0619cd886793573eb72f))

### Fix

* fix: remove role exclusion from gitignore, to reflect changes in diff ([`9f3c593`](https://github.com/weka/weka-operator/commit/9f3c5933e157d543f6fe969d6bed0de4502ab355))

* fix: add allocation initial log to list all applicable hosts ([`083c0b8`](https://github.com/weka/weka-operator/commit/083c0b865ae68d4ae45eb2b418a71defc3b2cd7f))

## v0.53.1 (2024-04-24)

### Fix

* fix: align versions 0.53.1 ([`11794c2`](https://github.com/weka/weka-operator/commit/11794c239398389b811aec1a236e7caf83adfe98))

## v0.53.0 (2024-04-24)

### Feature

* feat: enable support of coresNum ([`f3cd8f0`](https://github.com/weka/weka-operator/commit/f3cd8f023261293b0d137bea4299bcd9298e0b8b))

## v0.52.4 (2024-04-24)

### Fix

* fix: helm-chart enableClusterApi fix var name ([`f420f8c`](https://github.com/weka/weka-operator/commit/f420f8c1366078c5a79a096c1a10aede607d8767))

## v0.52.3 (2024-04-24)

### Fix

* fix: enableClusterApi helm chart fix ([`74390fc`](https://github.com/weka/weka-operator/commit/74390fc851d6103589bfb106382dfa8fab9099ca))

## v0.52.2 (2024-04-24)

### Fix

* fix: quote boolean enableClusterAPI ([`be1ef81`](https://github.com/weka/weka-operator/commit/be1ef814979d0d7b6c3c1798959175cc7bd4b4e2))

## v0.52.1 (2024-04-24)

### Chore

* chore: convert to table-driven tests (#120)

This converts the Ginkgo style tests to stdlib tests.  We get better test coverage for less work with this scheme. ([`320beef`](https://github.com/weka/weka-operator/commit/320beef637bb904f23efd0c9f09b1c68178d066c))

* chore: convert to table-driven tests ([`4586781`](https://github.com/weka/weka-operator/commit/458678114eedf311d925a9a02b90084ecf093c87))

### Fix

* fix: align values.yaml with release name ([`97c2c62`](https://github.com/weka/weka-operator/commit/97c2c628ffb74f4bf256e028b315c4e704681caf))

* fix: return goreleaser pipeline tests ([`0e1ef98`](https://github.com/weka/weka-operator/commit/0e1ef985386ea41db00d9a6fa662e7bd0f87d974))

## v0.52.0 (2024-04-24)

### Feature

* feat: 4.2.10 support and cpu policies defaults ([`811518c`](https://github.com/weka/weka-operator/commit/811518cac99fe5a7c62ceba18a55619932d28234))

## v0.51.0 (2024-04-24)

### Feature

* feat: suppport 4.2.10 based image ([`5e1ccff`](https://github.com/weka/weka-operator/commit/5e1ccff9af4dd0c064d2d0dc98f2093909c2e081))

## v0.50.0 (2024-04-22)

### Feature

* feat: option to force cpu policy by topology ([`97eea44`](https://github.com/weka/weka-operator/commit/97eea44d22fc3b55092d5cec8594d12f77977bc7))

## v0.49.0 (2024-04-22)

### Feature

* feat: nodes discovery, first use: clients dedicated/dedicated_ht ([`13b1142`](https://github.com/weka/weka-operator/commit/13b1142ab528c0a26274109ce498d8eccf959f62))

## v0.48.0 (2024-04-17)

### Chore

* chore: fix oci_single_container.yaml ([`e05aeb4`](https://github.com/weka/weka-operator/commit/e05aeb4a1d7113bcf197c794045cd3d5cccf5de1))

### Feature

* feat: add logrotate every 10 minutes ([`607b78c`](https://github.com/weka/weka-operator/commit/607b78c17088ae1bccb912ca2b0b12c75e35248f))

* feat: log separation by level

/dev/stdout: notice and up
/var/log/syslog: all, rotated
/var/log/error: error and up, rotated ([`7117db3`](https://github.com/weka/weka-operator/commit/7117db37d7f6fcf5e0986919ba754c5d9c9151e2))

* feat: wait for clusterization of container only if owned ([`6250f8a`](https://github.com/weka/weka-operator/commit/6250f8add65f706c4938099035f9d38ea3ac8582))

### Fix

* fix: refactor trace config via weka_runtime.py (#128)

fix: refactor trace config via weka_runtime.py

wait for clusterization of container only if owned

chore: fix oci_single_container.yaml ([`fa1da5a`](https://github.com/weka/weka-operator/commit/fa1da5a2c4eb55a63c62a60d3727567ff1ed8ff7))

* fix: review comments ([`03647e1`](https://github.com/weka/weka-operator/commit/03647e1ca8ac82794d7ab8f101e7680333c319ac))

* fix: store logs in persistent location on host ([`e1ff6eb`](https://github.com/weka/weka-operator/commit/e1ff6eb0f65db2902faac19ff6a83e186182068e))

* fix: refactor trace config via weka_runtime.py ([`ed91b81`](https://github.com/weka/weka-operator/commit/ed91b81f151d7b442ca025c230372999b9258f1b))

## v0.47.0 (2024-04-17)

### Feature

* feat: resolve target ips by cluster ([`a3420ac`](https://github.com/weka/weka-operator/commit/a3420ac67c0269090cd5b1ebbeb00721da5ced5e))

## v0.46.2 (2024-04-17)

### Fix

* fix: update CRDs with changes ([`5c22bec`](https://github.com/weka/weka-operator/commit/5c22bec425a2cb6912f3bfd8be62d5d5c314804e))

## v0.46.1 (2024-04-16)

### Fix

* fix: make sure that client executes weka commands in authenticated way (#125) ([`d5b0a37`](https://github.com/weka/weka-operator/commit/d5b0a370f3dea5925aa3eb6fb5db6eae09cf1cb2))

* fix: make sure that client executes weka commands in authenticated way ([`faa0af7`](https://github.com/weka/weka-operator/commit/faa0af7b21ef7720071ca2e3e4769d1d15bda4af))

## v0.46.0 (2024-04-16)

### Chore

* chore: set tracesConfiguration defaults ([`36e46a1`](https://github.com/weka/weka-operator/commit/36e46a106bb02aeeaeba17d7c3d03bc93af77053))

* chore: update CRDs with tracesConfiguration ([`a77179d`](https://github.com/weka/weka-operator/commit/a77179dae5901339ead129585a7a06c72b714e5e))

* chore: update examples to have tracesConfiguration ([`2d8daf3`](https://github.com/weka/weka-operator/commit/2d8daf3d44d1351952e7c773a6b34051320d23a7))

* chore: update WekaContainer modes to constants ([`c07d171`](https://github.com/weka/weka-operator/commit/c07d171cd1dee8db5071111abeb9ee39993f5d96))

### Feature

* feat: add traces retention configuration to WekaCluster, WekaContainer, Client (#122)

feat: add traces retention configuration to WekaCluster, WekaContainer, Client

chore: update WekaContainer modes to constants

chore: update examples to have tracesConfiguration

chore: update CRDs with tracesConfiguration

chore: change WekaContainer mode to Enum

fix: fix SpanLogger.WithName() did not pass prefix to log name ([`67defbf`](https://github.com/weka/weka-operator/commit/67defbf2cd3bf7d5ea60fcddd2b37f8ea5df2eec))

* feat: add wekadumper configuration functions ([`03ecad4`](https://github.com/weka/weka-operator/commit/03ecad493f04fedeb101f2f1efee605850c85de7))

* feat: use ExecNamed(), ExecSensitive() instead of generic Exec() ([`4fc0147`](https://github.com/weka/weka-operator/commit/4fc014753f4639c431f66d2e30e14c8fa49ed86b))

* feat: introduce ExecNamed(), ExecSensitive() methods ([`f0cc55c`](https://github.com/weka/weka-operator/commit/f0cc55c04e7719267aa8138868c2832a57474156))

* feat: add traces retention configuration to WekaCluster, WekaContainer, Client ([`9d0d046`](https://github.com/weka/weka-operator/commit/9d0d0460cee4a010c1b4c3df6d7cc92c5de98326))

### Fix

* fix: modify tracedumper config to override ([`55e3f2b`](https://github.com/weka/weka-operator/commit/55e3f2b6cb45eb7314fedc31be4e9aae8bbda477))

* fix: move traces config before clusterization ([`b4540b0`](https://github.com/weka/weka-operator/commit/b4540b0358753fa3cc967926e567bd434fc4a204))

* fix: negative number on container resource memory when no hp set ([`86108aa`](https://github.com/weka/weka-operator/commit/86108aab70a63465c656a176191e277cb4b58285))

* fix: validate container name in reconcileWekaLocalStatus ([`6b1c74a`](https://github.com/weka/weka-operator/commit/6b1c74a0481d1ab6f72b5dcd6353915c560b1153))

* fix: negative resources when container.Spec.Hugepages was undefined ([`5927ac0`](https://github.com/weka/weka-operator/commit/5927ac0c2f48dda893d911a53820b7b96e1f521a))

* fix: fix SpanLogger.WithName() did not pass prefix to log name ([`c0b74f9`](https://github.com/weka/weka-operator/commit/c0b74f9b081f4b242b7568bbf3d3a50fb52f7c62))

## v0.45.0 (2024-04-16)

### Feature

* feat: set clients as dedicated_ht when set to auto (#126) ([`e965e64`](https://github.com/weka/weka-operator/commit/e965e64d287f2be03e21da558f8b9f7b77e4b355))

* feat: set clients as dedicated_ht when set to auto ([`702ec9c`](https://github.com/weka/weka-operator/commit/702ec9c07c350c2c1d8bd9222bca371eeb0dab2d))

## v0.44.3 (2024-04-16)

### Fix

* fix: add missing container modes (#119)

Adds missing container `Mode` values that were dropped in a merge conflict. ([`85f1898`](https://github.com/weka/weka-operator/commit/85f18982d3e16a5406f6351f50138271b108972b))

* fix: add missing container modes ([`0ae14e8`](https://github.com/weka/weka-operator/commit/0ae14e88c67d5b78e262602a1561d107b88072f6))

* fix: nil reconciler in suite test ([`4288c1f`](https://github.com/weka/weka-operator/commit/4288c1f253ff7914e65eb6271e89cbb1c8cfc15f))

## v0.44.2 (2024-04-16)

### Fix

* fix: nil reconciler in suite test ([`b8f966c`](https://github.com/weka/weka-operator/commit/b8f966cfcab1cd8570725c514ea804e964df2248))

## v0.44.1 (2024-04-16)

### Fix

* fix: propagate values set on-span-create into inner spans ([`9f24bcd`](https://github.com/weka/weka-operator/commit/9f24bcd28b5f2ddd4486f7af6183c8f4e9cb94b0))

## v0.44.0 (2024-04-15)

### Chore

* chore: remove per-reconciler logSpan and use global by context ([`19dc63e`](https://github.com/weka/weka-operator/commit/19dc63ec58b892d3b80f2e57f1aeedbccaba94a5))

* chore: adapt wekaCluster reconciler to new GetLogSpan ([`b62b4a5`](https://github.com/weka/weka-operator/commit/b62b4a52aabd8cde3f1867392745a35559eac24b))

* chore: more boot logs + async fix for copyng drivers ([`7b742ae`](https://github.com/weka/weka-operator/commit/7b742ae0d9673e2fc5532f8c81d18f31c318b8fe))

* chore: more boot logs + async fix for copyng drivers ([`00a365c`](https://github.com/weka/weka-operator/commit/00a365c8662309d0c9b6d50443c80b49a0fa5366))

* chore: more boot logs + async fix for copyng drivers ([`81ebfff`](https://github.com/weka/weka-operator/commit/81ebfffcfbf57e50448ee2a391af35349089c1cf))

* chore: chained cluster reconcile ([`4519f55`](https://github.com/weka/weka-operator/commit/4519f556814a2deb70fa9fdd7d856f2423e750b4))

* chore: added new AWS TF for sergey ([`c830bda`](https://github.com/weka/weka-operator/commit/c830bda5ea331f5e47d3b848095740c6606dbf47))

* chore: update otel to 1.25.0 ([`e22fd34`](https://github.com/weka/weka-operator/commit/e22fd348b7126a3ce3b835407aff64a4ec69e861))

* chore: consolidate samples (#113)

Removed obsolete samples.  Move the remainder to `examples`. ([`f79bea9`](https://github.com/weka/weka-operator/commit/f79bea949011263ba59660b721ba03ea60ff15db))

* chore: consolidate samples ([`973936c`](https://github.com/weka/weka-operator/commit/973936c86ff19a27eb695873c9489798ceb3dbc9))

### Feature

* feat: drop shared trace id after init finishes ([`60aa8fd`](https://github.com/weka/weka-operator/commit/60aa8fd351226609652f4ff6883f6f82cf8671d0))

* feat: chain reconciles across single trace ([`325c5be`](https://github.com/weka/weka-operator/commit/325c5beb42cc2230ac2275b6fefdec466351f1d5))

* feat: single GetLogSpan func that should be used as logger getter ([`fb65a5e`](https://github.com/weka/weka-operator/commit/fb65a5e7f5c782e1d1157a3be147b8d62cd63ca6))

* feat: add delve debugcontroller ([`8a0ebbe`](https://github.com/weka/weka-operator/commit/8a0ebbe65f25ca68e7cc968036da290937b574e8))

* feat: abstract logging and tracing ([`6634998`](https://github.com/weka/weka-operator/commit/663499865245ae9e9582c463540455457ebe93ed))

* feat: add 3 retries on SetCondition ([`c0b65a3`](https://github.com/weka/weka-operator/commit/c0b65a304c0d386e71870ec4460a124c15e5815a))

* feat: add span attributes and phase to WekaClusterReconcile ([`dd33348`](https://github.com/weka/weka-operator/commit/dd33348edbed00731aff82f5b257e67ae01ea19f))

* feat: introduce WekaClusterReconsiler.SetCondition() ([`b2e675b`](https://github.com/weka/weka-operator/commit/b2e675b48f43dc6d64ade5589bfe907b924fb3c1))

* feat: distinguish between ExitCode errors and other errors in Exec ([`722472d`](https://github.com/weka/weka-operator/commit/722472d46bfd3668b61e5462595a41d7495b5d88))

* feat: phase in container reconciler span attribute ([`5e1e978`](https://github.com/weka/weka-operator/commit/5e1e9780413d10983dd8305c040c99a9e1201bf0))

* feat: contextual logger for wekacluster, wekacontainer ([`9de1825`](https://github.com/weka/weka-operator/commit/9de1825104a2ea7401cf777c150a3f6259e3cd29))

* feat: configurable OTEL_EXPORTER_OTLP_ENDPOINT including in Makefile ([`3226d70`](https://github.com/weka/weka-operator/commit/3226d70305982737c09dae039b973cc230bc45ff))

* feat: additional traces

fix: add few more span events on exec. fix exec path in SpdyExecutor

feat: add more traces to agent, container, allocator etc.

feat: add traces to allocator

fix: missing end of span for EnsureClusterContainerIds

fix: skip management IP reconcillation check for non-backend containers

feat: add spans for wekacluster_controller ([`b761c71`](https://github.com/weka/weka-operator/commit/b761c7186bc62d36310bf196e3b928cda1a7bcfc))

* feat: initial instrumentation support for traces ([`cbe0c51`](https://github.com/weka/weka-operator/commit/cbe0c51a43bb47b5f5a2c97d016aa1bed8927e25))

### Fix

* fix: fix some tests after changing the WekaClusterController.SetupWithManager() ([`bede8f2`](https://github.com/weka/weka-operator/commit/bede8f2c73e43e7cc6b49d5252e9955d2785e099))

* fix: propagate logger with values via context ([`dc36ddf`](https://github.com/weka/weka-operator/commit/dc36ddf5a0a620588c8065d34ebfe437b5b05aee))

* fix: properly un-bind trace-id from wekacluster post-provision ([`6b490b6`](https://github.com/weka/weka-operator/commit/6b490b6d9e5370650fb5089a752650a83a8d8e36))

* fix: propagate main context to reconciler ([`90d1516`](https://github.com/weka/weka-operator/commit/90d1516eb7d3597f8317f36d27207050dfb58a16))

* fix: remove error during refreshContainer if not found ([`1cae8db`](https://github.com/weka/weka-operator/commit/1cae8db6c0597d30b812832b80d249016f410148))

* fix: separates values from operators in all logSpans, otherwise we have unlimited cardinality on span types ([`15409f2`](https://github.com/weka/weka-operator/commit/15409f2a72710b11eb82b5e2c8d1f96492bb0654))

* fix: fix unreachable code in LogSpan.WithValues() ([`699bf39`](https://github.com/weka/weka-operator/commit/699bf391eb3b7ef30752b8664df0fd817dbd3a4e))

* fix: fix Allocator test failing after modifying the logger interface ([`d276b47`](https://github.com/weka/weka-operator/commit/d276b473861755412479ce5085e47408bca46a91))

* fix: modify ClientReconciler to use LogSpan ([`3e4ce9b`](https://github.com/weka/weka-operator/commit/3e4ce9bdc7c69338bdd2c97249a8828c0fb01ede))

* fix: weka_runtime.py only print stdout/err if non-nil ([`62b756c`](https://github.com/weka/weka-operator/commit/62b756c03283547436f6e0378e65ad95e3b80e51))

* fix: change some span names ([`2a5aa84`](https://github.com/weka/weka-operator/commit/2a5aa8483eda75068585902cfa953a09eeed49db))

* fix: add printouts to executed commands in weka_runtime.py ([`1350f62`](https://github.com/weka/weka-operator/commit/1350f62bc3926b77e84ae9ab7f2b82c30331947a))

* fix: weka drivers copied into invalid location due to redundant &#39;$&#39; ([`aedbba1`](https://github.com/weka/weka-operator/commit/aedbba1bbcc20a611b7bbd61e685156ffb92d654))

* fix: informative SetCondition span name ([`d644c26`](https://github.com/weka/weka-operator/commit/d644c26b50477fe3c8b68d41d4536afa24788562))

* fix: move to next drive on signature

wip: local-test of execs

wip ([`ba1de7f`](https://github.com/weka/weka-operator/commit/ba1de7f55d0fbfca7929a112407726ce493d1804))

* fix: lint fix of missing new line

WIP: refactor/shared traces

wip: trace+span update

wip: trace+span update2 ([`97d4bbe`](https://github.com/weka/weka-operator/commit/97d4bbe60c2f4a5358e44354286cd641f40dd171))

* fix: modify OCI single container example ([`a95e239`](https://github.com/weka/weka-operator/commit/a95e239bc667ee5865093ba5760b031cb517515a))

* fix: support initSignCloudDrives (not only aws) ([`765b154`](https://github.com/weka/weka-operator/commit/765b1548c70ec63b32b92b0e4f73ef17ec193d0c))

* fix: image update to beta 10 ([`0b40f14`](https://github.com/weka/weka-operator/commit/0b40f14e05af92c5bc445f7456015b07272bbd98))

* fix: examples for oci cluster and builder container ([`207cefe`](https://github.com/weka/weka-operator/commit/207cefe765afa0d7651194114c1bb16285a96985))

## v0.43.2 (2024-04-14)

### Fix

* fix(api): disable api by default ([`6f63a9e`](https://github.com/weka/weka-operator/commit/6f63a9ebcded2fc6461ca2c5319b33d9f78f31fc))

## v0.43.1 (2024-04-11)

### Build

* build: multi-region aws images (#110)

AWS image build for `us-east-1` and `eu-west-1`. ([`e0c43cd`](https://github.com/weka/weka-operator/commit/e0c43cdd2be3ef1df86fe34b23e7a31cbeca2612))

* build: multi-region aws images ([`b5e99d6`](https://github.com/weka/weka-operator/commit/b5e99d68f6c14da479b2de991527d02081a6b3a1))

### Fix

* fix: remove ensuredrivers condition (#111)

Removes CondEnsureDrivers from container.  @tigrawap is this what you had in mind? ([`03bd9e3`](https://github.com/weka/weka-operator/commit/03bd9e3cdcf0e98ba4a9804fadbe922e4f808f0f))

* fix: remove drives condition from client containers ([`b861330`](https://github.com/weka/weka-operator/commit/b86133014ab5d661e9c6fe37dd05c60985d7f2ff))

## v0.43.0 (2024-04-11)

### Chore

* chore: fix tests (#102)

basic test coverage for new controllers ([`7a28545`](https://github.com/weka/weka-operator/commit/7a2854559894d0e446638c162f838fb599941a23))

* chore: fix tests ([`1bbae82`](https://github.com/weka/weka-operator/commit/1bbae82f41484ff5fd9de9c3ccb7c5b69cd85dff))

### Ci

* ci: remove flaky test ([`d1964ad`](https://github.com/weka/weka-operator/commit/d1964ad1dfeccd7ca2fe633b3379e3958b389473))

### Feature

* feat(container): additional setup command args (#106)

Cluster adds `driveAppendSetupCommand` and `computeAppendSetupCommand`. Container adds `appendSetupCommand`. ([`9d1d7ea`](https://github.com/weka/weka-operator/commit/9d1d7ea67fbc76d4724f96a5285929be6cda6cd2))

* feat(container): additional setup command args ([`79b969b`](https://github.com/weka/weka-operator/commit/79b969b68c39e743056e1f6433a3b33a1bee758e))

## v0.42.1 (2024-04-11)

### Chore

* chore: more boot logs + async fix for copyng drivers ([`ce4dc8d`](https://github.com/weka/weka-operator/commit/ce4dc8d753ded46ec431694b28ccdc3c33639681))

* chore: manual setup of signoz (#116) ([`0a34226`](https://github.com/weka/weka-operator/commit/0a3422661c66b2c34dd774731da8756db984f4ca))

* chore: manual setup of signoz ([`4894d41`](https://github.com/weka/weka-operator/commit/4894d4103f26eb8c69e4c580f575615749a3cd43))

### Fix

* fix: build driver boot flow fixes, wait for agent (#118) ([`03b8852`](https://github.com/weka/weka-operator/commit/03b8852c271e3cd34044fe4b79f10d0d65be91fe))

* fix: build driver boot flow fixes, wait for agent ([`5013f0e`](https://github.com/weka/weka-operator/commit/5013f0efe0d7b97bd44e9a5f81015a287281669f))

* fix: drivers container fixes ([`209de47`](https://github.com/weka/weka-operator/commit/209de47507930148b17bbae44a6382c753064042))

### Unknown

* signals and drivers handling2 (#117) ([`a2165f1`](https://github.com/weka/weka-operator/commit/a2165f13287b52cc485d6dcf30fc013cd211a71d))

* signals and drivers handling2 ([`49b8c7a`](https://github.com/weka/weka-operator/commit/49b8c7a4936a4889dd6ece1c6a5dfffdd82817aa))

## v0.42.0 (2024-04-10)

### Feature

* feat: no-supervisor/simplify scripts to python ([`4a763bc`](https://github.com/weka/weka-operator/commit/4a763bc7eb5a5d3b2f498d1add42582ecb4c4867))

## v0.41.0 (2024-04-10)

### Chore

* chore: explicit reconcile requre on errors, typos fixes (#109) ([`994ef6f`](https://github.com/weka/weka-operator/commit/994ef6f80bd389c210f528a4ed7b901c4965bd11))

* chore: explicit reconcile requre on errors, typos fixes ([`34a4a62`](https://github.com/weka/weka-operator/commit/34a4a62c56ebb2ed03fa28f02c4ca203dd03029f))

* chore(packer):  more regions, needs templating ([`de28726`](https://github.com/weka/weka-operator/commit/de2872647481975ecea8ee2d03a2307ce64dc5be))

### Feature

* feat: cpu policies (#108) ([`2f0853e`](https://github.com/weka/weka-operator/commit/2f0853ed7b4a0c4466b8c29053c05ab13e180ca9))

* feat: cpu policies ([`fd08c40`](https://github.com/weka/weka-operator/commit/fd08c40ab83156c80658f651c002e1a450a0211c))

## v0.40.3 (2024-04-02)

### Fix

* fix: infer namespace 2 (#105) ([`f556a57`](https://github.com/weka/weka-operator/commit/f556a57bd225ef869eb157c24731300a104a6060))

* fix: infer namespace 2 ([`8dad9b4`](https://github.com/weka/weka-operator/commit/8dad9b41113feac415fd86bfbedd28b144f3f30e))

## v0.40.2 (2024-04-02)

### Fix

* fix: infer namespace from helm, and not by prefix, (#104) ([`775d424`](https://github.com/weka/weka-operator/commit/775d4247c5668460c9ad5acd502455b37e7a0f57))

* fix: infer namespace from helm, and not by prefix, ([`c753869`](https://github.com/weka/weka-operator/commit/c753869022e932f7258844c24ad3d98d12823984))

## v0.40.1 (2024-04-02)

### Fix

* fix: remove templating from values.yaml ([`81885d5`](https://github.com/weka/weka-operator/commit/81885d531a7b8a7946c8af4330ed63d84dc9ec1d))

## v0.40.0 (2024-04-02)

### Chore

* chore: disable clusterPodExec by-bad-query instead of panic ([`5479222`](https://github.com/weka/weka-operator/commit/54792228ca499f0076047b0cd363874b14dc09d3))

* chore: block getPodForCluster until ownership query ([`f28c57c`](https://github.com/weka/weka-operator/commit/f28c57c1056390c041beacf597c5cd61e06c6d97))

* chore: cleanup tf resources ([`8f1fdd9`](https://github.com/weka/weka-operator/commit/8f1fdd95933b2b0c96e5da645b40482b0dbf16bf))

### Documentation

* docs: add deployment and release to readme ([`7a266c3`](https://github.com/weka/weka-operator/commit/7a266c34a7301f4a0cd00dd7710b9dc0c6fde078))

### Feature

* feat(scheduler): align ports for cluster+role across whole cluster ([`53d89bf`](https://github.com/weka/weka-operator/commit/53d89bf12d2359eb5cf69cb58c03fcf0adcef8da))

## v0.39.0 (2024-04-02)

### Feature

* feat(api): add exec to change password ([`99bdad2`](https://github.com/weka/weka-operator/commit/99bdad2b5f1ef85d41bec60e76a6dfe164e63dd1))

## v0.38.0 (2024-04-01)

### Feature

* feat: driver-loader: remove once done injecting ([`9941838`](https://github.com/weka/weka-operator/commit/9941838146545b3dce906e5f7cc405b12b21453d))

## v0.37.0 (2024-04-01)

### Feature

* feat: allocator: spread and collocate by owner ([`22f1243`](https://github.com/weka/weka-operator/commit/22f12438b796a8377c841a2fcb057ca478dda45f))

## v0.36.0 (2024-04-01)

### Feature

* feat: clients: shortcircuit to rely on wekacontainer ([`4b01d67`](https://github.com/weka/weka-operator/commit/4b01d671764172035304dc29662eac9ffa009467))

## v0.35.0 (2024-04-01)

### Feature

* feat: aws cluster, drivers dist ([`808d287`](https://github.com/weka/weka-operator/commit/808d287ad4c6904a565ef1aa4cce83e7e3d3374d))

## v0.34.0 (2024-04-01)

### Feature

* feat: aws-backends custom image/cluster ([`1378850`](https://github.com/weka/weka-operator/commit/1378850bc1e72b1a36d6287d35ddffe4635e2ad8))

## v0.33.0 (2024-04-01)

### Chore

* chore: delete nodelabeler in more places ([`e0c98ea`](https://github.com/weka/weka-operator/commit/e0c98ea72a9d2fb51b2dc1d68a44a087116346cc))

* chore: disabled build of nodelabeller ([`e3344ed`](https://github.com/weka/weka-operator/commit/e3344ed56014dcf7128be8a0a4077b73cbcefc05))

### Feature

* feat(api): cluster-create adjustments ([`137fdb1`](https://github.com/weka/weka-operator/commit/137fdb1b18a7f13a099c4e6857cfe57c1dd77ec4))

## v0.32.0 (2024-03-27)

### Chore

* chore: rename/disable non-used code ([`a8baf94`](https://github.com/weka/weka-operator/commit/a8baf94f8cf81f31126fee0346105d9c02dd6410))

### Feature

* feat(api): create a cluster ([`402d7e4`](https://github.com/weka/weka-operator/commit/402d7e4ff38f7970af33595b949e8fad796f9fba))

## v0.31.0 (2024-03-27)

### Feature

* feat: create new users/remove admin user ([`d6d59ba`](https://github.com/weka/weka-operator/commit/d6d59ba234a818a2a975be48902734d08ff61dd8))

## v0.30.2 (2024-03-27)

### Fix

* fix: fix to detach api start from reconcile loop ([`e17d76d`](https://github.com/weka/weka-operator/commit/e17d76dfdbac5b6df11e9947f6a37705c8143815))

## v0.30.1 (2024-03-27)

### Fix

* fix: shutdown fix ([`d2307d7`](https://github.com/weka/weka-operator/commit/d2307d7abef2ead53b4a10d5c718ec8f71576b88))

## v0.30.0 (2024-03-27)

### Feature

* feat: update password ([`d40dbf2`](https://github.com/weka/weka-operator/commit/d40dbf2c1fd21e393fad4910f3b161742eee4c3f))

## v0.29.0 (2024-03-27)

### Feature

* feat: get cluster status ([`707434d`](https://github.com/weka/weka-operator/commit/707434d4e2c0dcb817806a8d02ad6e193c10e910))

## v0.28.0 (2024-03-27)

### Chore

* chore(api): add json rest library
chore(api): manage api lifecycle as a controller ([`88a27be`](https://github.com/weka/weka-operator/commit/88a27be1c14e76ea215cc8d46fe52180c488de14))

* chore(api): add json rest library
chore(api): manage api lifecycle as a controller ([`0398e38`](https://github.com/weka/weka-operator/commit/0398e383d49ace7be482ec0c04fa20c4434091ec))

### Feature

* feat: get cluster rest api ([`e9b0f9d`](https://github.com/weka/weka-operator/commit/e9b0f9d78a2ebc1ca0676292b69cf83eef1aa6e8))

* feat: rest api endpoint ([`f42914c`](https://github.com/weka/weka-operator/commit/f42914c44656f93179f3aa2a404809444596fe15))

## v0.27.1 (2024-03-27)

### Fix

* fix: shutdown loop fix ([`a9be61c`](https://github.com/weka/weka-operator/commit/a9be61c31059f9cfaed1c35aec309c6494eeb5b3))

## v0.27.0 (2024-03-27)

### Chore

* chore: remove second cluster from devbox ([`a89c4ef`](https://github.com/weka/weka-operator/commit/a89c4efe8e412b8331996705d552ba7461deedce))

### Feature

* feat: add driver claim object ([`eafcda2`](https://github.com/weka/weka-operator/commit/eafcda28228ebbf7e02d5480f6fed39395944bba))

## v0.26.2 (2024-03-26)

### Fix

* fix: weka container not stopping on pod termination ([`6667681`](https://github.com/weka/weka-operator/commit/6667681c9dbe9c231e586240237aca7f955f1e5d))

## v0.26.1 (2024-03-26)

### Fix

* fix: allow empty clusterID ([`7d213d7`](https://github.com/weka/weka-operator/commit/7d213d767be70a36310001741668e3bf7c34f49a))

## v0.26.0 (2024-03-25)

### Feature

* feat: nvme drives/support hugepages/configs ([`9dd487a`](https://github.com/weka/weka-operator/commit/9dd487a73b368e1f3d0ce9f4c98255a7795909cc))

### Fix

* fix: traps/pids aligment ([`30c3ff1`](https://github.com/weka/weka-operator/commit/30c3ff10de2e8e618ac707cc6eabbcdcf1579673))

* fix: syslog wrapper ([`6bdc399`](https://github.com/weka/weka-operator/commit/6bdc39948974a00bf3ebb1b440f351505964138d))

## v0.25.0 (2024-03-25)

### Feature

* feat: drives control loop. one-time signing of drives for weka to use is pre-requisite ([`8f5ee51`](https://github.com/weka/weka-operator/commit/8f5ee5185de3149e37aafb9adff0480bf5fb06dc))

## v0.24.0 (2024-03-25)

### Feature

* feat: add different terminationGracePeriods based on role

- drive can go down slower than 30 sec ([`0695785`](https://github.com/weka/weka-operator/commit/0695785b6bcb4e85da52c00db598e13d4bc98bce))

* feat: persist /opt/weka/traces,diags, logs/container, container dirs ([`e6ca64a`](https://github.com/weka/weka-operator/commit/e6ca64ae44cd6c0defafe1bf67680b989936c676))

* feat: put syslog-ng config inside configmap ([`fc0e7d8`](https://github.com/weka/weka-operator/commit/fc0e7d879c919ef1bc9f6cef793e333eb5c72f08))

### Fix

* fix: modify persistence path to /opt/k8s-weka/containers/UID ([`043d05d`](https://github.com/weka/weka-operator/commit/043d05d98cd6b6855f972b99d2936626255281ad))

* fix: use container UID in persistence path ([`6a8395a`](https://github.com/weka/weka-operator/commit/6a8395a95435d94b90ff1262679599b4b30ac7d8))

* fix: make sure dist dir is created after mount bind ([`26242e4`](https://github.com/weka/weka-operator/commit/26242e4d670239e0fb3a759257954b174827413e))

* fix: add exact command printout for weka local setup container ([`317c4ca`](https://github.com/weka/weka-operator/commit/317c4caf2cf47117cd3edd9aa627ecfb506edf5b))

* fix: add process name to stdout ([`fd3b445`](https://github.com/weka/weka-operator/commit/fd3b445113550e53db376a2487495228cbfd38fa))

* fix: modify persistence dir logic to be compatible with stateless and persistent nodes ([`62b0227`](https://github.com/weka/weka-operator/commit/62b02271059fe0b0c757f2224d292f0ad8779da2))

* fix: do not start weka-container before weka agent is initialized ([`ff6716b`](https://github.com/weka/weka-operator/commit/ff6716bab9cc51e71228ab2a15d27a06d08b9d30))

* fix: wait for explicit WEKA_AGENT_PID ([`b63c039`](https://github.com/weka/weka-operator/commit/b63c039917885b0202c7f9c0e72e34c80809591f))

* fix: correctly add script name in start-weka-container logging ([`782e58d`](https://github.com/weka/weka-operator/commit/782e58d991785de50c77fff2be8e0494eefcf721))

* fix: prettify start-weka-agent.sh and make it exit with SIGTERM

- pass SIGTERM to weka agent
- add logging on start-weka-agent.sh
- add script name to log output ([`fec99aa`](https://github.com/weka/weka-operator/commit/fec99aa6db1470662642c5d7cf0787cbe1c0bd22))

* fix: persistence that survices pod deletion ([`e44f9d7`](https://github.com/weka/weka-operator/commit/e44f9d7c527b25b1d9c19f58f81793e40ccc4615))

* fix: remove logs from persistence ([`148c1b9`](https://github.com/weka/weka-operator/commit/148c1b9c5c3bd91a4bc25e8df9a82a286b9e8694))

* fix: modify persistent directory structure to match native ([`84c6922`](https://github.com/weka/weka-operator/commit/84c6922a210e31259a960c6f0587b9197bb13a27))

* fix: persistence takes a pod name instead of weka container name ([`3235b9b`](https://github.com/weka/weka-operator/commit/3235b9b1f1fec4e18d24880b4502739d3007711d))

* fix: persistence for analytics, support, events, data ([`caa0c36`](https://github.com/weka/weka-operator/commit/caa0c367e67c53839515deacc74090340859cbb8))

* fix: syslog-ng.conf remove binding to port 514 ([`c509add`](https://github.com/weka/weka-operator/commit/c509add2a95a3726174faf6f2f4293e0b499e5bf))

* fix: syslog-ng.conf to use a correct domain socket path and create if missing ([`87872bb`](https://github.com/weka/weka-operator/commit/87872bba77ab9c18d724e9039cb354892551fd92))

## v0.23.0 (2024-03-24)

### Feature

* feat: templates and topology ([`f592f12`](https://github.com/weka/weka-operator/commit/f592f12270942e5d2a0d8515be5ec137b7a88e5a))

## v0.22.1 (2024-03-21)

### Fix

* fix:  adjusting values for two clusters on phical box ([`cda06b5`](https://github.com/weka/weka-operator/commit/cda06b5faefa5c169debcdabdfad76ee1a46810c))

## v0.22.0 (2024-03-21)

### Feature

* feat: replace statuses us with conditions for cleaner flow and cleaner steps ([`010c5ed`](https://github.com/weka/weka-operator/commit/010c5edea7cf29d1807b4d8683bad0f7584a3bf9))

## v0.21.0 (2024-03-21)

### Feature

* feat: drive add and start io ([`f055def`](https://github.com/weka/weka-operator/commit/f055def1d78d07fcc60ac328b7ba93f3e6d0f0f8))

## v0.20.0 (2024-03-20)

### Feature

* feat: populate cluster side container id ([`82d612c`](https://github.com/weka/weka-operator/commit/82d612ce88355003f1c6640c23b1dac7d8c7309d))

## v0.19.0 (2024-03-20)

### Feature

* feat: cluster loop till configure state, drive add is next ([`cc01222`](https://github.com/weka/weka-operator/commit/cc012226c794b65f0be46752bbe8491a984e89e2))

## v0.18.0 (2024-03-20)

### Chore

* chore: refactor management ip into a function (#66)

chore: refactor management ip into a function

feat(container): track wekacontainer status

Example:
```yaml
...
Status:
  Management IP:  10.222.98.0
  Status:         &#34;READY&#34;

``` ([`c372751`](https://github.com/weka/weka-operator/commit/c372751a3ec91e0e40eef50d921f7c6534f04878))

* chore: refactor management ip into a function ([`e240d71`](https://github.com/weka/weka-operator/commit/e240d71299eb57990e8d4046781c96edc50c5ac9))

### Feature

* feat(container): track wekacontainer status ([`e00f3bd`](https://github.com/weka/weka-operator/commit/e00f3bdf671d1af60d316781c507ba4423960ad7))

## v0.17.0 (2024-03-20)

### Feature

* feat: copy boot-scripts from operator namespace to target ([`d3a22e8`](https://github.com/weka/weka-operator/commit/d3a22e80a350925728bbd4dcbde29fd7acf917ba))

* feat: single configmap with all boot scripts in Helm chart ([`18ce2b8`](https://github.com/weka/weka-operator/commit/18ce2b8e52fe60693366130f7cf4a67d7aab4dd1))

## v0.16.0 (2024-03-20)

### Feature

* feat: flags to support udp/oci, examples and auto cluster create ([`f6f505f`](https://github.com/weka/weka-operator/commit/f6f505f7b679215cfbc6345c6e3e12af3f72205a))

## v0.15.0 (2024-03-19)

### Feature

* feat: standartize on weka-container and update of management ip ([`4cb86dd`](https://github.com/weka/weka-operator/commit/4cb86dd1958158f7b077a0fee40c1ac8bf055feb))

* feat: wekaContainer status IP field ([`6702757`](https://github.com/weka/weka-operator/commit/67027576487aeab30e8a65ba5fd98a234313575f))

## v0.14.0 (2024-03-19)

### Build

* build: add missing environment variables to dev build step ([`395e3bb`](https://github.com/weka/weka-operator/commit/395e3bbedcde761c57d9e0641a20d1233645fe9f))

### Chore

* chore: re-enable tests on goreleaser level, so they will run once they
are proper ([`fb37908`](https://github.com/weka/weka-operator/commit/fb37908ff53a09b3537fe60a54b2754c5b303074))

* chore: buildx/disable tests to advance ([`422c7c4`](https://github.com/weka/weka-operator/commit/422c7c467ff68be801e0c79e8c2f6ef54f1458b1))

* chore(container): update container to use anton&#39;s pod implementation ([`cf93c9d`](https://github.com/weka/weka-operator/commit/cf93c9da7255d5b5f77f7bb5fae50833f17c73a5))

* chore: reset to pre-scheduling/build flow aligment ([`502fb72`](https://github.com/weka/weka-operator/commit/502fb72a8d61512e9f618d18e5dde6f5cd5f4de7))

### Feature

* feat: generate cluster controller ([`0ae0841`](https://github.com/weka/weka-operator/commit/0ae084126ef57632392c3b64b84396a692674fdd))

### Fix

* fix: use non exclusive cpus ([`04b80ae`](https://github.com/weka/weka-operator/commit/04b80ae3a60cd25674398e277fa06222c9e345b8))

### Test

* test: disabled test suite by renaming, to recover once environment figured out ([`41d891b`](https://github.com/weka/weka-operator/commit/41d891b51c39a455d3f051ca6ba0b689e9779eca))

* test: disable all tests for now, until re-enabled one by one with definition ([`36122c3`](https://github.com/weka/weka-operator/commit/36122c3aee04ebe5c67f34a7cd10b1ed80c50786))

* test: fix cluster and backend tests ([`6517924`](https://github.com/weka/weka-operator/commit/6517924d677cf02d9eac2e29b5dec970256ffd89))

* test: enable container tests ([`fe7348b`](https://github.com/weka/weka-operator/commit/fe7348b4f8f00b441e18d06c2862e28e39b6aacf))

### Unknown

* Merge pull request #56 from weka/WEKAPP-370845/mbp/crd-tests

[WEKAPP-370845] Activate pending tests ([`abef8c7`](https://github.com/weka/weka-operator/commit/abef8c71779ca34fc79a57ae1ed0fa9488f4b433))

## v0.13.0 (2024-03-13)

### Build

* build: update eks tf ([`cab29ed`](https://github.com/weka/weka-operator/commit/cab29ed59b5182c53a2aab5a28b1376f57b47d92))

### Chore

* chore(deps): update deps ([`0d9c6ce`](https://github.com/weka/weka-operator/commit/0d9c6cec0209d9f921df6552fe0774d6c3f624d0))

* chore: move api types to pkg ([`957da63`](https://github.com/weka/weka-operator/commit/957da639bdac85ee8fd9f408b9ca5ea06a2bf67b))

* chore: helper tasks for makefile ([`5c26584`](https://github.com/weka/weka-operator/commit/5c26584e5c1b9ebf875545bfaa69fae2cfca29a9))

* chore(build): update build tooling for multiple binaries

fix: bad merge ([`b561967`](https://github.com/weka/weka-operator/commit/b56196771fe2d5901027ab41ad97473f4c503c0c))

* chore: replace legacy cluster with eks node pool ([`f7967d5`](https://github.com/weka/weka-operator/commit/f7967d50583c697afe6dc96bf9e20cc0a633a9c2))

### Feature

* feat: pin pass-through container to node ([`dc59fd4`](https://github.com/weka/weka-operator/commit/dc59fd4472fc3b23f86ef5586dce8035c03c7b49))

* feat(container): initial pass-through implementation for container ([`b49cf42`](https://github.com/weka/weka-operator/commit/b49cf425601a12afd85434184c96ae94dc6ac4b2))

* feat(backend): add container crd (wip)

- Instantiate containers from cluster controller
- Use owner ref and labels to show relationships

wip: assign containers to nodes

wip: add test coverage to cluster

wip: add test coverage to backend

tests for iteration ([`557a82e`](https://github.com/weka/weka-operator/commit/557a82e18574812b29d913bc7b30bc956a8f017c))

* feat: add drive crd

Uses labeller to generate instances

Remove boilerplate ([`064e806`](https://github.com/weka/weka-operator/commit/064e8063c1a6d48c3f033b37629c844516424b5e))

* feat: backend node crd

wip: report drive allocation

wip: start tracking drive count in backend

wip: log found drives

wip: add label to backend nodes ([`31cbd91`](https://github.com/weka/weka-operator/commit/31cbd91b35602a3212b87d8f4ad855632dda6c97))

* feat: cluster crd ([`215c086`](https://github.com/weka/weka-operator/commit/215c086e15004de51853d53154f8121c1225b1c1))

* feat: add node-labeller

The node labeller captures information about the backend and sets
corresponding labels on worker node objects ([`4187a9b`](https://github.com/weka/weka-operator/commit/4187a9b8b7d10f31030577b76004236f464f6e72))

### Test

* test: install envtest

test: add envtest to ci

fix envtest paths ([`6852a28`](https://github.com/weka/weka-operator/commit/6852a28524b15713cb7073c3a14d88ea65f9a6c8))

### Unknown

* Merge pull request #53 from weka/MT-43/mbp/container-crd

Independent containers ([`608f9be`](https://github.com/weka/weka-operator/commit/608f9becbaa51937f325d57157c89092b5f9a30d))

## v0.12.0 (2024-03-13)

### Feature

* feat: add node-labeller

The node labeller captures information about the backend and sets
corresponding labels on worker node objects ([`eaf51ca`](https://github.com/weka/weka-operator/commit/eaf51ca21be4732b1cb8572327dcb092fd4a29ec))

### Unknown

* Merge pull request #55 from weka/MT-43/mbp/node-labeller

feat: add node-labeller ([`b025982`](https://github.com/weka/weka-operator/commit/b02598287599c6f3e8e033d79da31c3543f54448))

## v0.11.1 (2024-03-07)

### Build

* build: update oci playbooks for recent changes ([`dc92218`](https://github.com/weka/weka-operator/commit/dc922187a45dd2ab690b3bc25d9829994df8c775))

### Fix

* fix: update daemonset when client crd changes ([`59971dd`](https://github.com/weka/weka-operator/commit/59971dde620d67f7aaf9d08b3cdfb98fd71000b2))

### Unknown

* Merge pull request #50 from weka/WEKAPP-373765/mbp/upgrade

[WEKAPP-373765] Update DaemonSet when client object changes ([`722c758`](https://github.com/weka/weka-operator/commit/722c7588df26437390a76961e3256c6231d70596))

## v0.11.0 (2024-03-06)

### Build

* build: build with goreleaser

Updates Makefile to use goreleaser instead of `go build` ([`620406f`](https://github.com/weka/weka-operator/commit/620406fbee00e8a68fe421fb7292fd7076361a0f))

### Chore

* chore(agent): remove unused config ([`6ff2587`](https://github.com/weka/weka-operator/commit/6ff2587eaaeeed3237df818bdc3a812fad6c5ba8))

* chore(lab): update playbooks for physical ([`0c3d47a`](https://github.com/weka/weka-operator/commit/0c3d47a3678c0c157722a4d2227b91e0d0ed6497))

* chore(build): provision physical cluster

configure k8s resources for operator

templatize image pull secret

upload drivers from local

wip: ubuntu hosts ([`fb1a361`](https://github.com/weka/weka-operator/commit/fb1a361be1c572b47f05b3104c72d821b90d3de6))

* chore(lab): add ansible playbook to build an oci lab ([`25570a6`](https://github.com/weka/weka-operator/commit/25570a65403beb2a9e19f799ef4da3519dfd9e7b))

### Ci

* ci: set permissions for semanticrelease ([`2a5b73f`](https://github.com/weka/weka-operator/commit/2a5b73f5123813ebdffe85682a3a68f066794906))

* ci: fix permissions on commitlint ([`004950f`](https://github.com/weka/weka-operator/commit/004950ff07796c8f97ad084a66264a4b03361468))

* ci: update release file to match dev ([`ce737f5`](https://github.com/weka/weka-operator/commit/ce737f5ac7f6b3ec974cf9c959341192e63d8606))

### Documentation

* docs: readme for ansible playbooks ([`4a3d6dc`](https://github.com/weka/weka-operator/commit/4a3d6dc5f6eb26842fa341ef1a8136c3f0122c04))

### Feature

* feat(agent): update memory allocation for mlnx iface ([`0d503dd`](https://github.com/weka/weka-operator/commit/0d503dda152958aafe0189cc563f6f47c3b216c6))

* feat(client): pass interface name to agent pod ([`eee867b`](https://github.com/weka/weka-operator/commit/eee867b82ab3b82b5887037a4b374f4b7030966d))

### Test

* test: update backend ips ([`b280413`](https://github.com/weka/weka-operator/commit/b280413c1d8c667acc0e26d5d8df57b6dc43f85a))

### Unknown

* Merge pull request #49 from weka/WEKAPP-371721/mbp/inject-interface-name

[WEKAPP-371721] Support for physical cluster with mlnx ([`ab0c396`](https://github.com/weka/weka-operator/commit/ab0c396ab54500742e03cde05d78db66c23ba71b))

* Merge pull request #47 from weka/drive-unload

chore(lab): add ansible playbook to build an oci lab ([`1d35766`](https://github.com/weka/weka-operator/commit/1d35766ae7858cf5a23a9a692efe646a878eaf01))

## v0.10.3 (2024-02-16)

### Ci

* ci: remove kustomize step from package ([`18e10c7`](https://github.com/weka/weka-operator/commit/18e10c71a821d6173def7520526f7f6db7d4a389))

### Fix

* fix: remove namespace from helm chart
fix: split crd into chart crd folder

This required splitting the generated manifest file and removing
kustomize from the tool-chain ([`3384ffc`](https://github.com/weka/weka-operator/commit/3384ffc76e9b86fa3373ba013ae060affbcd2e63))

### Unknown

* Merge pull request #45 from weka/helm-crd

fix helm layout to be more idiomatic ([`7407621`](https://github.com/weka/weka-operator/commit/740762106eb71246084bc4a51f3f9b31ca9fd8ab))

## v0.10.2 (2024-02-15)

### Fix

* fix: pass backend port to driver downloader ([`f6c29ff`](https://github.com/weka/weka-operator/commit/f6c29ff696f8348b7fd948faf37e77f64511ed93))

### Unknown

* Merge pull request #44 from weka/driver-download-port

fix: pass backend port to driver downloader ([`8b54795`](https://github.com/weka/weka-operator/commit/8b5479525366c984324b1c1fd3725bd3106ee54d))

## v0.10.1 (2024-02-14)

### Chore

* chore: projectionist configuration ([`9e60ed0`](https://github.com/weka/weka-operator/commit/9e60ed03c0072e2229ec888b9e1ff85b233e7ea3))

### Ci

* ci: run unit tests ([`f2898ef`](https://github.com/weka/weka-operator/commit/f2898ef5815bd214915d850820f8c524a9ea645f))

### Fix

* fix: remove undefined host-root mount point ([`094abc4`](https://github.com/weka/weka-operator/commit/094abc48686c9d7a934957dcddc880939e1cb74c))

* fix: remove test dependency on running cluster ([`d014b05`](https://github.com/weka/weka-operator/commit/d014b05f6952c6df08a6e707206dc1b60173700b))

### Unknown

* Merge pull request #43 from weka/missing-volumes

Fix missing volume configuration ([`58874ae`](https://github.com/weka/weka-operator/commit/58874ae788d37a2af3b0931a6296136f48c6694f))

## v0.10.0 (2024-02-13)

### Chore

* chore: remove unused volumes ([`741294a`](https://github.com/weka/weka-operator/commit/741294a926316765f243ab11f8311926cf9a9c65))

### Feature

* feat: remove cores config

Cores configuration is removed in favor of auto-detection in the latest
agent build.

wip: allow ssh into worker nodes ([`603b450`](https://github.com/weka/weka-operator/commit/603b45064e1c0a0c2cb2c1abda3a256421a80c52))

### Unknown

* Merge pull request #39 from weka/autodetect-cores

autodetect cores ([`5af8c22`](https://github.com/weka/weka-operator/commit/5af8c22968c3aa16656c29d340e98e3b1d830610))

## v0.9.1 (2024-02-13)

### Build

* build: provision drivers for download

wip: lab build improvements ([`09e0cd6`](https://github.com/weka/weka-operator/commit/09e0cd658fdc0cc44d5756ced4410d630785aac4))

* build: enable ssh into eks nodes

wip: ingress rule ([`4ad8cc3`](https://github.com/weka/weka-operator/commit/4ad8cc3d29c6f6721077a6d1213546e5f2eba64b))

* build: added terraform and ansible to build an eks cluster ([`06b0e8e`](https://github.com/weka/weka-operator/commit/06b0e8edeef5a05330b4b94549071911ddf33bf8))

### Fix

* fix: replace kmm with init container ([`4949054`](https://github.com/weka/weka-operator/commit/4949054e8cbc043065a181599d7f371d15291775))

### Unknown

* Merge pull request #41 from weka/MT-1/mbp/download-kmods

[MT-1] download drivers using init-container ([`d773bf7`](https://github.com/weka/weka-operator/commit/d773bf7a343579a084c5cbfb53c213f40ab4bdda))

* Merge pull request #40 from weka/tf-lab

Build a test environment in AWS using EKS ([`0f7e52d`](https://github.com/weka/weka-operator/commit/0f7e52d9ca942d54a89111d20f719f98dc2aa1bc))

## v0.9.0 (2024-02-05)

### Feature

* feat: expose `--base-port` as parameter on CRD ([`33c2b71`](https://github.com/weka/weka-operator/commit/33c2b71900b6281638fc3c0062257f0e100aa561))

### Unknown

* Merge pull request #38 from weka/port-arg

feat: expose `--base-port` as parameter on CRD ([`1136360`](https://github.com/weka/weka-operator/commit/1136360f317d88b6fb8ddaa9437a5eaeba690695))

## v0.8.0 (2024-01-25)

### Feature

* feat: add support for core-id argument ([`61c7778`](https://github.com/weka/weka-operator/commit/61c7778b2c39d1a8d93b1ef7c67799a6e04e1963))

### Unknown

* Merge pull request #37 from weka/core-ids-config

feat: add support for core-id argument ([`24f6216`](https://github.com/weka/weka-operator/commit/24f6216affb6aeaf3ba6bbb0ccae5e75df8ff76b))

## v0.7.1 (2024-01-24)

### Chore

* chore: quote template for linter ([`214bcae`](https://github.com/weka/weka-operator/commit/214bcaec8511ca85102eda06bd6fc92810ffad28))

### Ci

* ci: don&#39;t lint helm version field ([`f35ea80`](https://github.com/weka/weka-operator/commit/f35ea8047bd13892ef80174518589962cd628a07))

### Fix

* fix: add mpin_user driver ([`ea3275c`](https://github.com/weka/weka-operator/commit/ea3275c05d116b6a3f59a910ee131997b087ef4e))

### Unknown

* Merge pull request #35 from weka/mpin-user-driver

fix: add mpin_user driver ([`204c6ac`](https://github.com/weka/weka-operator/commit/204c6ac2c839e6edcb6f5e0a30c2a7da0bd108ec))

* Merge pull request #34 from weka/lintable-yaml

chore: quote template for linter ([`d139a8f`](https://github.com/weka/weka-operator/commit/d139a8faf39d25fafd3127815ffc996c04698614))

## v0.7.0 (2024-01-23)

### Feature

* feat: inject weka-cli credentials using CRD

I added username and password fields to the CRD that accept a
secretKeyRef. Use standard secretKeyRef syntax to inject the username
and password. These will become WEKA_USERNAME and WEKA_PASSWORD in the
container environment. ([`83195dd`](https://github.com/weka/weka-operator/commit/83195ddd14a340c7efb3909ef28ca51721e71c17))

### Unknown

* Merge pull request #33 from weka/credentials

feat: inject weka-cli credentials using CRD ([`55cadcb`](https://github.com/weka/weka-operator/commit/55cadcb609e86419defa14a5719903572a9078f1))

## v0.6.5 (2024-01-22)

### Fix

* fix: run processes under supervisord ([`8ec31cc`](https://github.com/weka/weka-operator/commit/8ec31ccdffe95d6d275799a2fa6f4365fe14d767))

### Unknown

* Merge pull request #31 from weka/go-supervisor

fix: run processes under supervisord ([`1715e81`](https://github.com/weka/weka-operator/commit/1715e819b9eb0278d34cf3649093c11bbed5df13))

## v0.6.4 (2024-01-19)

### Fix

* fix: incorrect app name in release dockerfile ([`58cfdef`](https://github.com/weka/weka-operator/commit/58cfdef974537837e96acaa8c41af50d74657e64))

### Unknown

* Merge pull request #28 from weka/start-error

fix: incorrect app name in release dockerfile ([`e5e7afc`](https://github.com/weka/weka-operator/commit/e5e7afc08718a62d1878e286f7d1727ff2cedece))

## v0.6.3 (2024-01-19)

### Fix

* fix: specify user in release dockerfile

This is intended to fix configuration errors in Kubernetes relating to
runAsNonRoot ([`2826a97`](https://github.com/weka/weka-operator/commit/2826a97a3a6e6df01f9a92e36fe81351ba36a6cb))

### Unknown

* Merge pull request #27 from weka/release-user

fix: specify user in release dockerfile ([`7fd6f21`](https://github.com/weka/weka-operator/commit/7fd6f2108297622b3c12be6ca10cf949108e42ed))

## v0.6.2 (2024-01-19)

### Fix

* fix: remove &#39;v&#39; from all image versions ([`776af0b`](https://github.com/weka/weka-operator/commit/776af0b748d12e40329699d2a64d57192c90ba7f))

### Unknown

* Merge pull request #26 from weka/image-version

fix: remove &#39;v&#39; from all image versions ([`cc0845a`](https://github.com/weka/weka-operator/commit/cc0845a51ba47d7592d36578b0e8b9a29728a7d9))

## v0.6.1 (2024-01-19)

### Fix

* fix: add &#39;v&#39; prefix to chart image version ([`53a8c29`](https://github.com/weka/weka-operator/commit/53a8c29a5f392eb53eb806b0c3c9e22f29231280))

### Unknown

* Merge pull request #25 from weka/chart-image-version

fix: add &#39;v&#39; prefix to chart image version ([`277e594`](https://github.com/weka/weka-operator/commit/277e594439f9958afd0f7e6b4dc853930e928f89))

## v0.6.0 (2024-01-19)

### Chore

* chore: formatting ([`3ec1b1a`](https://github.com/weka/weka-operator/commit/3ec1b1a41e06e097772675f1eeeaecb41f6df89a))

* chore: remove generated files

This includes a template and a role definition.  These are generated at
build time and should not be in git.  Also updated Dockerfile to ensure
they are generated in docker builds as well. ([`d47bf87`](https://github.com/weka/weka-operator/commit/d47bf871282be5b774efb9618f872353fc28ce7a))

* chore: ignore unpacked helm charts ([`1497ec7`](https://github.com/weka/weka-operator/commit/1497ec7a89787f2b50dc26642dbbcad33ccd49ba))

### Feature

* feat: replace systemd with dumb-init ([`6543ad3`](https://github.com/weka/weka-operator/commit/6543ad33ccb19d07576ae37e0aed1f82ccdda251))

### Fix

* fix: grant operator permissions on daemonset ([`768f820`](https://github.com/weka/weka-operator/commit/768f820bad176ac84a0d7862a4cb3bfa7664dad6))

* fix: add util folder to docker build ([`b5a1725`](https://github.com/weka/weka-operator/commit/b5a172558d5da421752106b862444afcfe51a222))

### Unknown

* Merge pull request #24 from weka/dumb-init

Replace systemd with dumb-init ([`ddaf424`](https://github.com/weka/weka-operator/commit/ddaf42463a835be32812e6cacebe724a2bd788d5))

## v0.5.0 (2024-01-17)

### Chore

* chore: refactoring ([`faebc63`](https://github.com/weka/weka-operator/commit/faebc6351f4c8b5403acdbcb9b676dfecddc85b9))

### Feature

* feat: record container list during reconciliation ([`5202a9a`](https://github.com/weka/weka-operator/commit/5202a9ada1c960277d8749fc807758cf20568682))

* feat: update conditions during reconciliation loop ([`3fba76f`](https://github.com/weka/weka-operator/commit/3fba76f9c15e9815b2175cccf436e117a0406ed9))

* feat: record reconciliation events ([`bb2b74e`](https://github.com/weka/weka-operator/commit/bb2b74e9a96279d2b89ba369013fc99c444bd1a3))

### Fix

* fix: reconcile process list until processes are ready ([`46680e9`](https://github.com/weka/weka-operator/commit/46680e9e6ad21a85d54c51848da1a0203d294794))

### Unknown

* Merge pull request #17 from weka/status-reporting

status reporting ([`a55e626`](https://github.com/weka/weka-operator/commit/a55e6261c5fe7065cf4bc84b5cf55cc6bf6b8d64))

## v0.4.1 (2024-01-11)

### Fix

* fix: correct release version for make chart ([`6455d85`](https://github.com/weka/weka-operator/commit/6455d8578a12fdab1100eb99d4790cf487fc918f))

### Unknown

* Merge pull request #15 from weka/fix-release

fix: correct release version for make chart ([`bb4bc7f`](https://github.com/weka/weka-operator/commit/bb4bc7f8b46ba5ada3e7f72bcfa222527c231095))

## v0.4.0 (2024-01-11)

### Ci

* ci: add semrel version to release chart ([`dd8a172`](https://github.com/weka/weka-operator/commit/dd8a1723441ec09df580ff4ccbbceac5ce32f8a8))

* ci: build prs using goreleaser ([`643939f`](https://github.com/weka/weka-operator/commit/643939f496474a387dca901e255d3ac8909dd995))

* ci: pass GITHUB_TOKEN to goreleaser ([`1fa25b1`](https://github.com/weka/weka-operator/commit/1fa25b10aa04956b0028a9e6f446747abf76b050))

### Feature

* feat: enable multiple nodes by converting to a daemonset

The agent will be run on every registered node in the cluster ([`43ffef4`](https://github.com/weka/weka-operator/commit/43ffef49ef888e30d31a17337bc7327acd95daed))

### Fix

* fix: block startup until finished joining ([`e9c88f9`](https://github.com/weka/weka-operator/commit/e9c88f92c00ed72a5786d04a969d3bd471a79d8c))

### Unknown

* Merge pull request #12 from weka/daemonset

feat: enable multiple nodes by converting to a daemonset ([`bb9f6bb`](https://github.com/weka/weka-operator/commit/bb9f6bbc73e70b8885bbfad9fc38199e58bd2109))

## v0.3.0 (2024-01-10)

### Ci

* ci: run semantic release after helm release ([`19cb7f3`](https://github.com/weka/weka-operator/commit/19cb7f3be29dc6ec02c4704627b555016c3dea1f))

### Feature

* feat: auto-discover management IPs ([`30108c8`](https://github.com/weka/weka-operator/commit/30108c81e6d33b8e92b37c38f8a90e673f7967b4))

### Unknown

* Merge pull request #11 from weka/autodisco-managementip

feat: auto-discover management IPs ([`509020c`](https://github.com/weka/weka-operator/commit/509020c547add0dc6afd2bb5911618ecc9ad9cab))

* Merge pull request #13 from weka/remove-backend.net

fix: remove unused net config ([`e60922e`](https://github.com/weka/weka-operator/commit/e60922e8f0c0db670fd2bad6c7b137c9e40650ab))

* Merge branch &#39;main&#39; into remove-backend.net ([`6591be7`](https://github.com/weka/weka-operator/commit/6591be7c15ef423c042e2224eb941ffbc2b6c946))

* Fallback to 4.2.7.64-based naming of release to be compatible with
backends ([`a41c4a6`](https://github.com/weka/weka-operator/commit/a41c4a66d70a1bd73928a1ccfeea4fcddcb18be2))

## v0.2.0 (2024-01-10)

### Ci

* ci: build project with goreleaser

This change inits the goreleaser and uses it to build images ([`b539ba8`](https://github.com/weka/weka-operator/commit/b539ba8e9b109689796b2029f623b34c0ef8fdf6))

### Feature

* feat: use local k8s registry for image build caching ([`1d7284a`](https://github.com/weka/weka-operator/commit/1d7284a6836c4014b7800c8d4af9002955304db8))

* feat: generic image for downloading kernel modules from backends ([`25932d1`](https://github.com/weka/weka-operator/commit/25932d1203036a43065a136c81324512a7908152))

### Fix

* fix: remove unused net config ([`802ac7e`](https://github.com/weka/weka-operator/commit/802ac7e1896e7a187cc462688ada19abcdaf32f8))

* fix: remove unused net config ([`30e4994`](https://github.com/weka/weka-operator/commit/30e4994801dc60e3d727ed3cec49c3f072f7d18e))

### Unknown

* beta release versions update/cleanups ([`9620080`](https://github.com/weka/weka-operator/commit/9620080700ed59c9951950d77445cc7d02e6a0b3))

* Merge pull request #10 from weka/goreleaser

ci: Build project with goreleaser ([`60187eb`](https://github.com/weka/weka-operator/commit/60187eba4c0af35ed5772cdf88bf4009f68a2cae))

* Merge pull request #9 from weka/bump-release

chore: bump version ([`a95a990`](https://github.com/weka/weka-operator/commit/a95a9900f0e33cdf649aabe14235fc2d1987f986))

## v0.1.1 (2024-01-04)

### Chore

* chore(ci): enforce conventional commits

Add conventional commit validation and basic release automation using
github actions.  Commit conventions are enforced using `commitlint`.
This allows the use of semantic-release on merge to main. ([`3b89421`](https://github.com/weka/weka-operator/commit/3b894218628b44dbd72e03dd29214ab790ee236d))

### Fix

* fix: bump version ([`9834d92`](https://github.com/weka/weka-operator/commit/9834d9273f91598c645fca740798ef8c44a4437a))

### Unknown

* Merge pull request #8 from weka/conventional-commits

Add conventional commits validation to repo ([`2511290`](https://github.com/weka/weka-operator/commit/25112909aa5703c8266957619d2d4a5e9ff77b74))

* Merge pull request #7 from weka/chart

Create a Helm Chart to package the operator ([`6bff4a7`](https://github.com/weka/weka-operator/commit/6bff4a71faa5763480257446f82eab8b5cb3ac71))

* NOTES: need image pull secret ([`f3c65a9`](https://github.com/weka/weka-operator/commit/f3c65a9f2fac9a2d85b619bf0645c15530b66266))

* Add NOTES ([`f74c72e`](https://github.com/weka/weka-operator/commit/f74c72e375a3d4119ad42c2ade6877eeaab1f6db))

* Remove bundle ([`8179694`](https://github.com/weka/weka-operator/commit/81796945d082dad97b4660d7cd69d1e6f6314d95))

* Remove kustomize from asdf ([`31c1b8a`](https://github.com/weka/weka-operator/commit/31c1b8aff9b13886338338c3af214718b5fb41ce))

* Only package on merge to main ([`fc55556`](https://github.com/weka/weka-operator/commit/fc5555606285d503a50bc1b78580e6154b6fbcee))

* Docker login quay.io ([`f80e224`](https://github.com/weka/weka-operator/commit/f80e22452b76a9ffac47da5f0bd5e974cbae9a4c))

* docker login ([`85c0272`](https://github.com/weka/weka-operator/commit/85c02723a4a996154270919e61c9fafad427be68))

* Remove buildx from chart dependencies ([`c4de63c`](https://github.com/weka/weka-operator/commit/c4de63c6b07d209409c2af2557425b0b5bb52db1))

* buildx -&gt; build and push ([`f1ff19d`](https://github.com/weka/weka-operator/commit/f1ff19d230798ae20e0c798236a265d4f0879ef6))

* Push tarball ([`eabfe7e`](https://github.com/weka/weka-operator/commit/eabfe7e18c8b6fea51aee1537682ddac4be4a8fe))

* Disable tests ([`91b6a54`](https://github.com/weka/weka-operator/commit/91b6a5499ba1ffa725fd52ddcdf68af872a73803))

* v1.8.0 ([`d6e5487`](https://github.com/weka/weka-operator/commit/d6e5487ea5f6a1f4a4e0d9d97895d79a0bcb029b))

* kind action ([`b5f9a89`](https://github.com/weka/weka-operator/commit/b5f9a89b5394ef2a8470f893af824750a187f85a))

* docker-buildx ([`051a8b4`](https://github.com/weka/weka-operator/commit/051a8b45b28596bb0d9c7416873d01cacf037a63))

* Make templates ([`9234c01`](https://github.com/weka/weka-operator/commit/9234c012eb9a7ae0d1069417c91b0b7cf8e6fdb0))

* separate make kustomize ([`8cd3d04`](https://github.com/weka/weka-operator/commit/8cd3d04b05959b100747a62d38c73799f99a567f))

* make manifests ([`3a1810d`](https://github.com/weka/weka-operator/commit/3a1810d7d7aed461b2efddb651cfeb415cccd181))

* make build ([`3502d7b`](https://github.com/weka/weka-operator/commit/3502d7b93b391268325c9c8721eb93a10a0e18a0))

* Use kustomize provided by make ([`2d39e00`](https://github.com/weka/weka-operator/commit/2d39e003d7bcfcf69cd7c2417776504823c10098))

* Label kustomize install task ([`220870f`](https://github.com/weka/weka-operator/commit/220870fd263630459451cad4d6916ba88f7d038f))

* Uncompress archive ([`7232973`](https://github.com/weka/weka-operator/commit/72329739f69f7e583b793166253bd455891b205a))

* Follow redirects ([`8bd4750`](https://github.com/weka/weka-operator/commit/8bd4750b4fad160120299fba82f7d53e9a7015c3))

* Remove kustomize action ([`0b0aa13`](https://github.com/weka/weka-operator/commit/0b0aa13c82500a75dc63e7cab49c0104243c0c3c))

* Download kustomize ([`e95fbe9`](https://github.com/weka/weka-operator/commit/e95fbe9c3c6857cb0852360cd4fc3eea506c20b5))

* action-kustomize v2 ([`2c0f7fd`](https://github.com/weka/weka-operator/commit/2c0f7fdb77cfd87a6159a61c798ce3809c282ebe))

* Install kustomize ([`5131bf9`](https://github.com/weka/weka-operator/commit/5131bf9f6e968510fdc5c85f1c1c0fe487f4d93f))

* WIP: Build chart ([`0a0bf65`](https://github.com/weka/weka-operator/commit/0a0bf6590d58100f821d359fef3555f071b1e017))

* Login to quay registry ([`5ecffe0`](https://github.com/weka/weka-operator/commit/5ecffe0238a240da4e94160a4694d0d4f64c1084))

* packages_with_index ([`0caec3b`](https://github.com/weka/weka-operator/commit/0caec3b1dd322c99b3cfc76d715bbc6704a13695))

* Package on push to main branch ([`2d3005b`](https://github.com/weka/weka-operator/commit/2d3005b86a5c42c456e7d843a361f910d17b052f))

* Separate linter job ([`a80e0da`](https://github.com/weka/weka-operator/commit/a80e0da33f41520789e57bc98beeb740c6d0b9ca))

* Add git configs ([`23ea1e3`](https://github.com/weka/weka-operator/commit/23ea1e353753a4a9cac0581f1dce9c3cb83a3762))

* Add release step ([`062a50c`](https://github.com/weka/weka-operator/commit/062a50ccb41399cac394c38b468103d842ec0252))

* Remove chart testing step ([`d257db4`](https://github.com/weka/weka-operator/commit/d257db4a2c6c2530db1e87dbce662e8ff99af2fe))

* change maintainer to github org name ([`c9e5ef2`](https://github.com/weka/weka-operator/commit/c9e5ef28eee7465c86dbd9503d0f796127198e08))

* Add maintainers ([`7500a7c`](https://github.com/weka/weka-operator/commit/7500a7ceb691794c86f095bee21a671e4bc85afd))

* Remove trailing whitespace ([`903d00c`](https://github.com/weka/weka-operator/commit/903d00cb040a9bf6d733b27b943b9324dc14e3af))

* Lint the chart in actions ([`89aa80e`](https://github.com/weka/weka-operator/commit/89aa80efbd67b4085287b4dd4a5ea022e6e2525c))

* Tag version 0.1.0 ([`a92c775`](https://github.com/weka/weka-operator/commit/a92c7752ce12479e21929bdda68f6877acd3a350))

* values for image pull secret ([`d42350b`](https://github.com/weka/weka-operator/commit/d42350b2d425de204976a6c07a95b796f3f240df))

* Add operator to helm chart ([`5878f79`](https://github.com/weka/weka-operator/commit/5878f7997e1cf42f8c33c1a146d8cd1a634f8c5a))

* WIP inject image pull secrets ([`2c56676`](https://github.com/weka/weka-operator/commit/2c56676028e797250f768badab95f9bacc64f641))

* remove pre-kustomized templates from chart ([`10fda96`](https://github.com/weka/weka-operator/commit/10fda963a561c42e5348d4d4cc31dfd1e5967f7d))

* build chart with static manifests ([`409d0c3`](https://github.com/weka/weka-operator/commit/409d0c3552ba7adb85a5d4af29e40394f01fd9ff))

* wip: adding a chart ([`982eb17`](https://github.com/weka/weka-operator/commit/982eb170d0185a017bdae1a5f24b4e14d6b1eb0a))

* wip: adding a chart ([`b657064`](https://github.com/weka/weka-operator/commit/b657064d5e88627c9882560aac358b29249ecea4))

* Merge pull request #6 from weka/envtest

Add envtest ([`f7cbe56`](https://github.com/weka/weka-operator/commit/f7cbe564f322ea4b4edbc457efc712415f2a4b66))

* Verify wekafsio module created ([`65b85f7`](https://github.com/weka/weka-operator/commit/65b85f7cb9b65173874db400fcf5c3eb10aade92))

* Create client in real cluster ([`110afe1`](https://github.com/weka/weka-operator/commit/110afe1e34ec5d9583242d09da741579663e37d9))

* WIP tests for controller ([`fcd1d55`](https://github.com/weka/weka-operator/commit/fcd1d55e21ae806848aea2f324e08da733046345))

* Add envtest ([`4f048ec`](https://github.com/weka/weka-operator/commit/4f048ec8120e421bfd026cbae04a95c9cc50caa1))

* Merge pull request #5 from weka/agent-pod

[DEVOPS-1595] Create an agent pod with a working weka client ([`0238530`](https://github.com/weka/weka-operator/commit/023853085660d95e3d4edfdf4122aa925c64e6b6))

* Update version string ([`df22973`](https://github.com/weka/weka-operator/commit/df2297318a668370374e86b15be01f64ba7843b9))

* Add required capabilities for hugepages ([`3afc0eb`](https://github.com/weka/weka-operator/commit/3afc0eb7aab29c5c712d7101a08ed346e5195783))

* Disable client container ([`35f88cf`](https://github.com/weka/weka-operator/commit/35f88cf06783c398a107491cc34f77452cffd8ae))

* whitepsace ([`0c02d15`](https://github.com/weka/weka-operator/commit/0c02d15a66f7d142ce1a9d56439246761c4c5059))

* Agent commang -&gt; systemd ([`e220c6f`](https://github.com/weka/weka-operator/commit/e220c6f643f4cc870e533ea8deab95826798aced))

* Requeue deployment until available ([`0198b38`](https://github.com/weka/weka-operator/commit/0198b381c3bbf2df0cdcfd2ef207a22ce50339a0))

* Requeue after each phase ([`029575c`](https://github.com/weka/weka-operator/commit/029575cce42b689030f3131ffbeeaa3646f7570b))

* Add process list to status ([`d06f825`](https://github.com/weka/weka-operator/commit/d06f825c7b6f4edf9176553a905895d333811615))

* Factor exec code into own method ([`a5196b0`](https://github.com/weka/weka-operator/commit/a5196b0ad95d91632a27d84e9f1f86949b60674a))

* Extract API key from client ([`13a9330`](https://github.com/weka/weka-operator/commit/13a9330620643f2811e19e08935701a873f7d614))

* WIP: Add status ([`b6a7ae0`](https://github.com/weka/weka-operator/commit/b6a7ae0b2fe83ffcb39d2f7af18d73b2ce87f069))

* Clean up resources on deletion ([`bace674`](https://github.com/weka/weka-operator/commit/bace6742483dd1c93d292290f29d3ca1f5f83715))

* Update version + debug flags ([`4f33e27`](https://github.com/weka/weka-operator/commit/4f33e27d149b97d0ab1cad3ffb485c7230d4154a))

* Add back hugepage 2 volume ([`f58245c`](https://github.com/weka/weka-operator/commit/f58245c0592768b9c74036f2a1d772d08bb91176))

* Match deployment to latest prototype yaml in wekapp ([`dee3388`](https://github.com/weka/weka-operator/commit/dee3388a6a2e281019cba5483db5233bc410f225))

* Factor resources into reconciliation phases ([`121a87b`](https://github.com/weka/weka-operator/commit/121a87b82b329c19b26cd9f5a8c6bc7ff2394f73))

* Add EnvTest ([`3d8bd26`](https://github.com/weka/weka-operator/commit/3d8bd26a01810ce058d3c47d8b6be9d818b01527))

* Start client and agent containers ([`8574870`](https://github.com/weka/weka-operator/commit/85748701fc8fe74c7c1906c159d73eb116998844))

* Enable debug in pod ([`0855f92`](https://github.com/weka/weka-operator/commit/0855f92106c2fc2d5a28a0d7c2847358bce01575))

* Add imagepullsecret to pod ([`b2ad603`](https://github.com/weka/weka-operator/commit/b2ad603fb4f9db7920c473702acf9c7164e29ceb))

* Add cleanup/stub finalizer ([`90e0980`](https://github.com/weka/weka-operator/commit/90e09809c75ef7314da43466e6955831b4bfae3a))

* Instantiate drivers ([`efd06b4`](https://github.com/weka/weka-operator/commit/efd06b4eb00761317610baaa3bdb16bc68c9d35c))

* Incorrect secret name ([`2cde8e5`](https://github.com/weka/weka-operator/commit/2cde8e5439c89cdfc37736fae7be84c5d0fb7df8))

* If driver is not found ([`2ebf0ab`](https://github.com/weka/weka-operator/commit/2ebf0ab826d6f0a1e426a58aa679aaf682fa7b0d))

* validate module options ([`31cea2a`](https://github.com/weka/weka-operator/commit/31cea2a5d204d5784eb39abbfa5911dbfec80c0e))

* Move image pull secret to top level ([`dbfd6d1`](https://github.com/weka/weka-operator/commit/dbfd6d12ff5e4ad12201c9c511c2f3b21c7abf54))

* Add modules helper to control loop ([`f76580f`](https://github.com/weka/weka-operator/commit/f76580f50552d9f9a85e15d673fd5ba70cb6931f))

* Add kmm dependency ([`cbfeca8`](https://github.com/weka/weka-operator/commit/cbfeca8e30358dbdc5e77cd5ee0b5f1baec63731))

* Define wekafs driver factories ([`503490b`](https://github.com/weka/weka-operator/commit/503490b3c8bc8d73d7e29aa33a6100f18737fd6f))

* Add Agent Deployment ([`fc4c72c`](https://github.com/weka/weka-operator/commit/fc4c72cbd21b57f8c2e67b3c53be7009f470133d))

* Merge pull request #3 from weka/minikube

minikube 1.32 ([`3aa6247`](https://github.com/weka/weka-operator/commit/3aa624749c1bf48fed989b6ac8b0327e0874b76b))

* minikube 1.32 ([`9cecb5f`](https://github.com/weka/weka-operator/commit/9cecb5f5a89944318cc41c1b203df0a3700504b4))

* Merge pull request #2 from weka/privileged-container

Run busybox pod as a privileged container ([`a381b9c`](https://github.com/weka/weka-operator/commit/a381b9c43abbe78cff6b3efe620fdde3009db741))

* Add example daemonset ([`3b8c148`](https://github.com/weka/weka-operator/commit/3b8c148141138664fc7ffd7132f243bbbf4bd565))

* Run Busybox as a privileged container ([`d00c27c`](https://github.com/weka/weka-operator/commit/d00c27c066b089d11e4ddb6bdb45aa83438e2efc))

* Ignore DS_Store ([`f9de554`](https://github.com/weka/weka-operator/commit/f9de5542f00f6cd164625d851c6a6f2fafb31828))

* Merge pull request #1 from weka/operator-sdk

Operator sdk ([`5284bb5`](https://github.com/weka/weka-operator/commit/5284bb571f7f31db6533d1c3472739f115ef1e8e))

* README ([`3db9820`](https://github.com/weka/weka-operator/commit/3db982021843e3ad1c58b4eb8cef4f918360d4e1))

* Create a busybox deployment ([`c8f45a4`](https://github.com/weka/weka-operator/commit/c8f45a4d2d19db871a0b90ea1d9bee9e52fa1ad3))

* Add log statements to Reconcile ([`b831e8a`](https://github.com/weka/weka-operator/commit/b831e8a3bd55bfde92cd009239c7762f90796350))

* Stub controller implementation ([`c754141`](https://github.com/weka/weka-operator/commit/c7541417be28a747423f2bee4666b5a3933715cc))

* Add Version to CRD ([`7f42c81`](https://github.com/weka/weka-operator/commit/7f42c815ef1ce746e95f37dbf57366557269d880))

* build bundle ([`c778090`](https://github.com/weka/weka-operator/commit/c7780908f7af508ff3c521e0be497e654c883cd9))

* create cluster with registry ([`42c7010`](https://github.com/weka/weka-operator/commit/42c70109af681a0e31ec7c7085a03b8f7552d261))

* script to create local registry ([`d533f26`](https://github.com/weka/weka-operator/commit/d533f26db3091a6409da40ac80b8a466a7164d6f))

* image at localhost ([`7bdd95c`](https://github.com/weka/weka-operator/commit/7bdd95ce95cb806da1f26ff2f9c6c5a9ad32cee1))

* make bundle ([`3084f5c`](https://github.com/weka/weka-operator/commit/3084f5cc29a561d0c2e3e0a074cfb65fc7206d45))

* create api ([`c78b86c`](https://github.com/weka/weka-operator/commit/c78b86c764ed4d94a594c32378b296e058975057))

* operator-sdk init ([`111b385`](https://github.com/weka/weka-operator/commit/111b385abe8c48a7b7a1d5f38bd9df6f76d5e302))

* Delete kubebuilder generated code ([`cd9e1dc`](https://github.com/weka/weka-operator/commit/cd9e1dcd62442eee73335693f5b344ea76ab5350))

* Add version property to crd ([`75da3e3`](https://github.com/weka/weka-operator/commit/75da3e30cbd4a54a7d1ea73d83d587ec576e2b85))

* add k9s ([`d5f73f7`](https://github.com/weka/weka-operator/commit/d5f73f736075051006aa2af4f78e0d1ffb055de4))

* generated ([`0f1c99f`](https://github.com/weka/weka-operator/commit/0f1c99f7cba3b2fdb7d422756b19e9249bb05cfc))

* create api ([`7f18aeb`](https://github.com/weka/weka-operator/commit/7f18aebaa6bef84ff159f8c9012ca11a97c20312))

* kubebuilder init ([`8d79c06`](https://github.com/weka/weka-operator/commit/8d79c0648a6ffca58d3acf5cfb2c99f7dd27fca8))

* prerequisites ([`462ec22`](https://github.com/weka/weka-operator/commit/462ec22bd95e3db653c2dc1d16ba4d1ada05f365))
