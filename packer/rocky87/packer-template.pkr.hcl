packer {
  required_plugins {
    amazon = {
      source  = "github.com/hashicorp/amazon"
      version = "~> 1"
    }
  }
}

locals {
  #  internal_users = ["303605160296","459693375476", "460079793829", "854561606399", "237520467869", "130745022161", "643793144496", "919961283311", "613151511434", "720378078651", "869376154687", "031156366157", "078726528415", "704541115166", "339712935457", "750977848747"]
  internal_users = []
  #  external_users = ["924994152927", "591822521499"]
  external_users = []
  all_users      = concat(local.internal_users, local.external_users)

  aws_regions = {
    "eu-west-1" = {
      ami       = "ami-07686f42f40b34957",
      ami_users = local.all_users
    }
    #    "us-east-1" = {
    #      ami = "ami-0757bdb3268077f9f",
    #      ami_users = local.all_users
    #    }
    #    "us-east-2" = {
    #      ami = "ami-0757bdb3268077f9f",
    #      ami_users = local.all_users
    #    }
    #    "us-west-2" = {
    #      ami = "ami-09d72b72587e6e07c",
    #      ami_users = local.all_users
    #    }
    #    "us-west-1" = {
    #      ami = "ami-061a7f6a95a250b09",
    #      ami_users = local.all_users
    #    }
  }


}

source "amazon-ebs" "weka-eks" {
  instance_type = "m6a.4xlarge"
  ssh_username  = "rocky"
  ami_name      = "weka-rocky8.7-${formatdate("YYYYMMDDHHmmss", timestamp())}"
  assume_role {
    role_arn = "arn:aws:iam::381492135989:role/Root"
  }
  ami_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = 200
    delete_on_termination = true
    volume_type           = "gp3"
  }
}

build {
  name = "weka-eks"
  dynamic "source" {
    for_each = local.aws_regions
    labels   = ["amazon-ebs.weka-eks"]
    content {
      region     = source.key
      source_ami = source.value.ami
      ami_users  = source.value.ami_users
    }
  }

  provisioner "file" {
    source      = "install_kernel_headers.sh"
    destination = "/tmp/install_kernel_headers.sh"
  }

  provisioner "shell" {
    inline = [
      "chmod +x /tmp/install_kernel_headers.sh",
      "sudo /tmp/install_kernel_headers.sh",
    ]
  }

  post-processor "manifest" {
    output = "manifest.json"
  }
}
