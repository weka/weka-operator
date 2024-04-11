packer {
  required_plugins {
    amazon = {
      source  = "github.com/hashicorp/amazon"
      version = "~> 1"
    }
  }
}

variables {
  aws_regions = {
    "eu-west-1" = {
      ami = "ami-0cca685d73cf4fd6b",
      ami_users = [],
    }
    "us-east-1" = {
      ami = "ami-0757bdb3268077f9f",
      ami_users = ["924994152927"]
    }
    "us-west-2" = {
      ami = "ami-09d72b72587e6e07c",
      ami_users = ["591822521499"]
    }
  }
}

source "amazon-ebs" "weka-eks" {
  instance_type = "m6a.4xlarge"
  ssh_username  = "ubuntu"
  ami_name      = "weka-eks-${formatdate("YYYYMMDDHHmmss", timestamp())}"
  assume_role {
    role_arn = "arn:aws:iam::381492135989:role/Root"
  }
}

build {
  name = "weka-eks"
  dynamic "source" {
    for_each = var.aws_regions
    labels = ["amazon-ebs.weka-eks"]
    content {
      region        = source.key
      source_ami    = source.value.ami
      ami_users     = source.value.ami_users
    }
  }

  provisioner "shell" {
    inline = [
      "sudo apt-get update",
      "sudo apt-get install -y linux-headers-5.15.0-1056-aws linux-image-5.15.0-1056-aws sed",
      "sudo grub-set-default \"Advanced options for Ubuntu>Ubuntu, with Linux 5.15.0-1056-aws\"",
      "sudo sed -i 's/GRUB_DEFAULT=0/GRUB_DEFAULT=saved/' /etc/default/grub",
      "sudo update-grub",
    ]
  }

  provisioner "shell" {
    inline = [
      "sudo reboot"
    ]
    pause_before      = "30s"
    expect_disconnect = true
  }

  provisioner "shell" {
    inline = [
      "uname -r",
      "cat /boot/grub/grub.cfg | grep menuentry",
    ]
  }
}
