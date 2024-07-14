#!/bin/bash

cat <<EOF | sudo tee /etc/yum.repos.d/rocky-vault.repo
[rocky-vault]
name=Rocky Linux \$releasever - Vault
baseurl=https://download.rockylinux.org/vault/rocky/8.7/BaseOS/x86_64/os/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-rockyofficial
EOF

sudo dnf clean all
dnf --disablerepo="*" --enablerepo="rocky-vault" install -y kernel-devel-`uname -r`
