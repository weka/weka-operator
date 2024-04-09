machines setup:

- `apt-get install -y linux-headers-5.15.0-1056-aws linux-image-5.15.0-1056-aws`
- `apt-mark hold linux-headers-5.15.0-1056-aws linux-image-5.15.0-1056-aws`
- `rm -f /boot/vmlinuz-6.5.0-*`
- `update-grub`


this part did not work because of mistake in the target kernel version, so such grub-set-default might still work, needs retesting
```
- ```DEBIAN_FRONTEND=noninteractive apt-remove -y linux-image-`uname -r` ```
- `grub-set-default "Advanced options for Ubuntu>Ubuntu, with Linux linux-image-5.15.0-1056-aws"`
- `update-grub`
```


#TODO: Back use of additional EBS device for /opt/k8s-weka (and maybe more)


# K8s Secrets:
- use kube.py to create pull-secret create command via get_kube_login  
- kubectl create secret...  
- Optionally: `kubectl patch serviceaccount default --namespace default -p '{"imagePullSecrets": [{"name": "quay-io-robot-secret"}]}'` for cases like direct kubectl run

quick snippet of what had to deploy into k8s and not run locally
```
kubectl create secret docker-registry quay-cred           --docker-server=quay.io           --docker-username=weka.io+weka_oc           --docker-password=TRUEPASSWORD           --docker-email="weka.io+weka_oc"           --namespace=weka-operator-system secret/quay-cred created
kubectl patch serviceaccount default --namespace weka-operator-system -p '{"imagePullSecrets": [{"name": "quay-cred"}]}'

```