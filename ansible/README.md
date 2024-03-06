# Kubernetes Lab Provisioning - Ansible

See also: [Kubernetes Lab Provisioning - Terraform](terraform/README.md)

## Prerequisites

These are the versions I use.
Others may work fine.

- Python 3.12
- Poetry 1.7.1

## Install

```bash
poetry install --no-root
```

## Usage

### Environment

You must define environment variables containing your `quay.io` credentials:
- `QUAY_USERNAME`
- `QUAY_PASSWORD`

These will be used to create an `imagePullSecret` in the Kubernetes cluster.

```bash
./physical.sh --weka-version 4.2.9.586-fd83fa18eff15f86fa344231aeed6b56
```

### How it works

This project contains three shell scripts: `eks.sh`, `oci.sh`, and `physical.sh`.
These build roughly equivalent Kubernetes clusters on AWS EKS, Oracle Cloud Infrastructure, and physical hardware, respectively.

### Inventory

#### Common Groups

- `all` - All hosts, used for common configuration.
- `frontend` - The frontend Kubernetes nodes.
- `backend` - The backend Weka nodes.

TODO: Explicitly support Kubernetes on backend nodes.
This will replace the traditional Weka installation.

#### EKS Dynamic Inventory

The `eks` playbook will query AWS and discover appropriate nodes when the playbook runs.
This assumes that the EKS cluster was created with terraform.


#### Driver Management

The playbooks copy drivers on the backend nodes to `/opt/weka/dist/drivers` on the same node.
They are archived as `tar.gz` files.
The operator will download them from this location at runtime.

#### Provided Kubernetes Resources

These playbooks provide the following Kubernetes resources:
- Namespace: `weka-operator-system`
- Image Pull Secret: `quay-cred`
- Weka CLI Secret: `weka-cli`

## Limitations

At the moment, these scripts are specific to my environment.
(ie. Things get named `mbp`).
If you want to use them, you'll need to generalize them.
