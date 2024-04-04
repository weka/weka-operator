# Packer Build

## Usage

First, initialize plugins:

```bash
packer init packer-template.pkr.hcl
```

Then, run the build:

```bash
packer build packer-template.pkr.hcl
```

## Multi-region builds

The packer template natively supports the following regions:

- `us-east-1`
- `us-west-2`
- `eu-west-1`

No special action is required to build in these regions.
To add a new region, add a new `amazone-ebs` builder block to the template.
Then reference this block in the `build.sources` list.
Example:

```hcl
source "amazon-ebs" "weka-eks-eu-west-1" {
  region        = "eu-west-1"
  # Note: amis are region-specific
  # Find an appropriate one using `aws ec2 describe-images`
  source_ami    = "ami-0cca685d73cf4fd6b"
  instance_type = "m6a.4xlarge"
  ssh_username  = "ubuntu"
  ami_name      = "weka-eks-${formatdate("YYYYMMDDHHmmss", timestamp())}"
  assume_role {
    role_arn = "arn:aws:iam::381492135989:role/Root"
  }
}

build {
  sources = [
    # ...
    "source.amazon-ebs.weka-eks-us-east-1",
    # ...
  ]
}
```

## Permissions

Launch permissions are granted to the following users:

| ID           | Name | Region    |
| ---          | ---  | ---       |
| 924994152927 |      | us-east-1 |
| 591822521499 |      | us-west-2 |
