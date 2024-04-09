# Define an EKS node group for the weka backend

locals {
  user_data = <<-EOF
              #!/bin/bash
              echo 6000 > /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages
              echo "vm.nr_hugepages=6000" >> /etc/sysctl.conf
              /usr/sbin/sysctl --system
              /etc/eks/bootstrap.sh ${aws_eks_cluster.eks.name} \
                --kubelet-extra-args '--cpu-manager-policy=static'
              EOF
}

resource "aws_eks_node_group" "weka_backend" {
  count = var.create_backend_node_group ? 1 : 0
  cluster_name    = aws_eks_cluster.eks.name
  node_group_name = "${local.prefix}-${local.cluster_name}-weka-backend-node-group-${count.index}"
  node_role_arn   = aws_iam_role.eks_role.arn
  subnet_ids      = [aws_subnet.weka_subnet1.id, aws_subnet.weka_subnet2.id]

  scaling_config {
    desired_size = 5
    max_size     = 5
    min_size     = 5
  }

  launch_template {
    id      = aws_launch_template.backend.id
    version = aws_launch_template.backend.latest_version
  }

  labels = {
    "weka.io/role" = "backend"
  }

  depends_on = [aws_eks_cluster.eks]
}

resource "aws_launch_template" "backend" {
  name_prefix   = "${local.prefix}-${local.cluster_name}-backend"
  image_id      = local.image_id
  instance_type = "i3en.6xlarge"
  key_name      = aws_key_pair.eks_key_pair.key_name

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_size           = 150
      volume_type           = "gp3"
      delete_on_termination = true
    }
  }

  credit_specification {
    cpu_credits = "standard"
  }

  #iam_instance_profile {
  #arn = local.instance_iam_profile_arn
  #}

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "enabled"
  }

  monitoring {
    enabled = true
  }

  network_interfaces {
    associate_public_ip_address = true
    delete_on_termination       = true
    device_index                = 0
    security_groups             = [data.aws_security_group.eks_control_plane.id, aws_security_group.eks_worker_nodes.id]
  }

  #placement {
  #availability_zone = data.aws_subnet.this[0].availability_zone
  #group_name        = local.backends_placement_group_name
  #}

  dynamic "tag_specifications" {
    for_each = ["instance", "network-interface", "volume"]
    content {
      resource_type = tag_specifications.value
      tags = merge({ "env" : "dev", "creator" : "tf" }, {
        Name                = "${local.prefix}-${local.cluster_name}-${tag_specifications.value}-backend"
        weka_cluster_name   = local.cluster_name
        weka_hostgroup_type = "backend"
      })
    }
  }
  user_data = base64encode(local.user_data)
}



resource "aws_launch_template" "builder" {
  name_prefix   = "${local.prefix}-${local.cluster_name}-builder"
  image_id      = local.image_id
  instance_type = "m6a.2xlarge"
  key_name      = aws_key_pair.eks_key_pair.key_name

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_size           = 50
      volume_type           = "gp3"
      delete_on_termination = true
    }
  }

  credit_specification {
    cpu_credits = "standard"
  }

  #iam_instance_profile {
  #arn = local.instance_iam_profile_arn
  #}

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
    instance_metadata_tags      = "enabled"
  }

  monitoring {
    enabled = true
  }

  network_interfaces {
    associate_public_ip_address = true
    delete_on_termination       = true
    device_index                = 0
    security_groups             = [data.aws_security_group.eks_control_plane.id, aws_security_group.eks_worker_nodes.id]
  }

  dynamic "tag_specifications" {
    for_each = ["instance", "network-interface", "volume"]
    content {
      resource_type = tag_specifications.value
      tags = merge({ "env" : "dev", "creator" : "tf" }, {
        Name                = "${local.prefix}-${local.cluster_name}-${tag_specifications.value}-builder"
        weka_cluster_name   = local.cluster_name
        weka_hostgroup_type = "builder"
      })
    }
  }
  user_data = base64encode(local.user_data)
}

resource "aws_eks_node_group" "weka_builder" {
  count = var.create_backend_node_group ? 1 : 0
  cluster_name    = aws_eks_cluster.eks.name
  node_group_name = "${local.prefix}-${local.cluster_name}-weka-builder-node-group-${count.index}"
  node_role_arn   = aws_iam_role.eks_role.arn
  subnet_ids      = [aws_subnet.weka_subnet1.id, aws_subnet.weka_subnet2.id]

  scaling_config {
    desired_size = 1
    max_size     = 1
    min_size     = 1
  }

  launch_template {
    id      = aws_launch_template.builder.id
    version = aws_launch_template.builder.latest_version
  }

  labels = {
    "weka.io/role" = "builder"
  }

  depends_on = [aws_eks_cluster.eks]
}

