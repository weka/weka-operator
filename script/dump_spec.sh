#!/bin/bash

# should be ran from directory that should get spec dumped into
# k8s cluster should be set in kubeconfig

cd doc/spec_dump || (echo "dump repo isn't found" && exit 1)

kubectl explain --recursive wekacontainer > wekacontainer.spec.txt
kubectl explain --recursive wekacluster > wekacluster.spec.txt
kubectl explain --recursive wekaclient > wekaclient.spec.txt
kubectl explain --recursive wekapolicy > wekapolicy.spec.txt
kubectl explain --recursive wekamanualoperation > wekamanualoperation.spec.txt

echo "specs dumped to doc/spec_dump"
