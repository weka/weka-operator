- Upgrade of weka cluster is done by changing image field in wekacluster CR
  - An indication of finished upgrade - status.lastAppliedImage field should get updated to latest version
  - Upgrade process will rotate all the pods in the cluster by updating the image field on wekacontainers that belong to weka cluster, one by one and waiting for lastAppliedImage to be updated on weka container
  - If not pods were recreated within 10 minutes - consider it failed
- Upgrade of weka clients is done by changing image field in wekaclient CR
    - An indication of finished upgrade - status.lastAppliedImage field should get updated to latest version
    - Upgrade process will upgrade wekaImage on all wekacontainers that belong to weka client, all at once, and each wekacontainer will replace own pod to latest image the moment there is no IOs active on it
- To test this properly - create wekacluster and wekaclient with toleration(rawToleration) to all taints with key weka.io/upgrade, this taint will be used to evict workload while preserving weka components
  - Once wekaclient image is cahnged - set this taint on all nodes that are matching, and validate that workload evicted and pods are replaced with new image

A convenient way to poll for progress of upgrade:
If nothing changes for like 10 minutes - it is a good indication that upgrade failed
```text
kubectl get wekacontainer -n test-upgrade -l weka.io/cluster-name=test-weka-upgrade-03190816-0j9x -o  custom-columns=NAME:.metadata.name,WEKA_SIDE_CONTAINER_NAME:.spec.name,IMAGE:.spec.image
NAME                                                                           UID                                            IMAGE
test-weka-upgrade-03190816-0j9x-compute-0b97ff3e-3726-4760-9885-4555e5518f11   computex0b97ff3ex3726x4760x9885x4555e5518f11   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-3f4477af-62ec-47cb-90de-cc0b42496d0f   computex3f4477afx62ecx47cbx90dexcc0b42496d0f   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-4a3b59dd-3b2f-46df-bfb6-52a2704f2485   computex4a3b59ddx3b2fx46dfxbfb6x52a2704f2485   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-4e00704c-aaed-4d2e-9928-f5aa42dfe8df   computex4e00704cxaaedx4d2ex9928xf5aa42dfe8df   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-529e8120-64e4-4669-886b-3843e9bc7343   computex529e8120x64e4x4669x886bx3843e9bc7343   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-58e38f99-04b6-410e-a494-51ef8380792e   computex58e38f99x04b6x410exa494x51ef8380792e   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-8e7c34ee-ad15-4e9b-a51e-bf4a16338a00   computex8e7c34eexad15x4e9bxa51exbf4a16338a00   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-compute-e976cf8c-9b1a-4d91-bc04-feca48d29e83   computexe976cf8cx9b1ax4d91xbc04xfeca48d29e83   quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-drive-10b7e8b3-c0f3-41b5-995c-f54201f17d46     drivex10b7e8b3xc0f3x41b5x995cxf54201f17d46     quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-drive-14d7ad53-112b-4533-8eeb-2a1fc3049822     drivex14d7ad53x112bx4533x8eebx2a1fc3049822     quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-drive-1ecf608b-8f61-4c17-8394-15b076510e8d     drivex1ecf608bx8f61x4c17x8394x15b076510e8d     quay.io/weka.io/weka-in-container:4.4.5.111-k8s
test-weka-upgrade-03190816-0j9x-drive-35f43037-7820-4dd1-ac61-27860fc93529     drivex35f43037x7820x4dd1xac61x27860fc93529     quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-drive-445fcbd6-45ed-4979-910c-37fcc8d024b6     drivex445fcbd6x45edx4979x910cx37fcc8d024b6     quay.io/weka.io/weka-in-container:4.4.5.111-k8s
test-weka-upgrade-03190816-0j9x-drive-4a995afa-aed6-4027-938c-6e8901208d1c     drivex4a995afaxaed6x4027x938cx6e8901208d1c     quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
test-weka-upgrade-03190816-0j9x-drive-64273898-f36d-4008-b0b3-7b110555894c     drivex64273898xf36dx4008xb0b3x7b110555894c     quay.io/weka.io/weka-in-container:4.4.5.111-k8s
test-weka-upgrade-03190816-0j9x-drive-6b4a5550-abed-4b9b-8b04-3dd5b2db18e7     drivex6b4a5550xabedx4b9bx8b04x3dd5b2db18e7     quay.io/weka.io/weka-in-container:4.4.2.157-k8s.2
```