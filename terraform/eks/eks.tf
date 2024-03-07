resource "aws_key_pair" "eks_key_pair" {
  key_name   = "${local.prefix}-${local.cluster_name}-eks-ssh-key"
  public_key = file("~/.ssh/weka_dev_ssh_key.pub")
}

data "aws_ssm_parameter" "eks_ami" {
  name = "/aws/service/eks/optimized-ami/1.29/amazon-linux-2/recommended/image_id"
}

# Create a k8s cluster to go with weka
resource "aws_eks_cluster" "eks" {
  name     = local.cluster_name
  role_arn = aws_iam_role.eks_role.arn
  vpc_config {
    subnet_ids = [aws_subnet.weka_subnet1.id, aws_subnet.weka_subnet2.id, aws_subnet.public.id]
  }

  version = local.kubernetes_version

  depends_on = [
    aws_iam_role_policy_attachment.weka-AmazonEKSClusterPolicy,
    aws_iam_role_policy_attachment.weka-AmazonEKSVPCResourceController,
  ]

}

# IAM Polices

data "aws_iam_policy_document" "assume_role" {
  statement {
    effect = "Allow"
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com", "ec2.amazonaws.com"]
    }
    actions = ["sts:AssumeRole"]
  }
}

resource "aws_iam_role" "eks_role" {
  name               = "${local.prefix}-${local.cluster_name}-eks-role"
  assume_role_policy = data.aws_iam_policy_document.assume_role.json
}

resource "aws_iam_role_policy_attachment" "weka-AmazonEKSClusterPolicy" {
  role       = aws_iam_role.eks_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_iam_role_policy_attachment" "weka-AmazonEKSVPCResourceController" {
  role       = aws_iam_role.eks_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSVPCResourceController"
}

# IAM Role for worker worker nodes
resource "aws_iam_role_policy_attachment" "weka-AmazonEKSWorkerNodePolicy" {
  role       = aws_iam_role.eks_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy"
}

resource "aws_iam_role_policy_attachment" "weka-AmazonEKS_CNI_Policy" {
  role       = aws_iam_role.eks_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy"
}

resource "aws_iam_role_policy_attachment" "weka-AmazonEC2ContainerRegistryReadOnly" {
  role       = aws_iam_role.eks_role.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

# Worker Nodes
resource "aws_eks_node_group" "weka_node_group" {
  cluster_name    = aws_eks_cluster.eks.name
  node_group_name = "${local.prefix}-${local.cluster_name}-weka-node-group"
  node_role_arn   = aws_iam_role.eks_role.arn
  subnet_ids      = [aws_subnet.weka_subnet1.id, aws_subnet.weka_subnet2.id]
  scaling_config {
    desired_size = 3
    max_size     = 7
    min_size     = 1
  }
  depends_on = [aws_eks_cluster.eks]

  launch_template {
    id      = aws_launch_template.worker_nodes.id
    version = aws_launch_template.worker_nodes.latest_version
  }

  # K8s labels
  labels = {
    "weka.io/role"                         = "client"
    "kmm.node.kubernetes.io/control-plane" = "true"
  }
}

resource "aws_launch_template" "worker_nodes" {
  name_prefix   = "${local.prefix}-${local.cluster_name}-eks-"
  instance_type = "m6i.xlarge"
  image_id      = data.aws_ssm_parameter.eks_ami.value
  key_name      = aws_key_pair.eks_key_pair.key_name

  block_device_mappings {
    device_name = "/dev/sda1"
    ebs {
      volume_size = 50
    }
  }

  capacity_reservation_specification {
    capacity_reservation_preference = "open"
  }

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name = "${local.prefix}-${local.cluster_name}-weka-node"
    }
  }

  user_data = base64encode(<<-EOF
              #!/bin/bash
              echo "vm.nr_hugepages=1024" >> /etc/sysctl.conf
              /usr/sbin/sysctl --system
              /etc/eks/bootstrap.sh ${aws_eks_cluster.eks.name} \
                --kubelet-extra-args '--cpu-manager-policy=static'
              EOF
  )
}

data "aws_security_group" "eks_control_plane" {
  id = aws_eks_cluster.eks.vpc_config[0].cluster_security_group_id
}

resource "aws_security_group_rule" "inbound_worker_to_control_plane" {
  type                     = "ingress"
  from_port                = 1025
  to_port                  = 65535
  protocol                 = "tcp"
  security_group_id        = data.aws_security_group.eks_control_plane.id
  source_security_group_id = aws_security_group.eks_worker_nodes.id
}

resource "aws_security_group" "eks_worker_nodes" {
  name        = "${local.prefix}-${local.cluster_name}-eks-worker-nodes"
  vpc_id      = aws_vpc.weka_vpc.id
  description = "EKS worker nodes security group"
}

resource "aws_security_group_rule" "inbound_control_plane_to_worker" {
  type                     = "ingress"
  from_port                = 1025
  to_port                  = 65535
  protocol                 = "tcp"
  security_group_id        = aws_security_group.eks_worker_nodes.id
  source_security_group_id = data.aws_security_group.eks_control_plane.id
}

resource "aws_security_group_rule" "all_traffic_between_workers" {
  type              = "ingress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.eks_worker_nodes.id
  self              = true
}

resource "aws_security_group_rule" "outbound_worker_nodes" {
  type              = "egress"
  from_port         = 0
  to_port           = 0
  protocol          = "-1"
  security_group_id = aws_security_group.eks_worker_nodes.id
  cidr_blocks       = ["0.0.0.0/0"]
}

# Inbound SSH from the internet
resource "aws_security_group_rule" "inbound_ssh" {
  type              = "ingress"
  from_port         = 22
  to_port           = 22
  protocol          = "tcp"
  security_group_id = data.aws_security_group.eks_control_plane.id
  cidr_blocks       = ["0.0.0.0/0"]
}

# Allow backend (10.0.2.0/24) to frontend (10.0.1.0/24) communication
resource "aws_security_group_rule" "inbound_weka" {
  type              = "ingress"
  from_port         = 0
  to_port           = 65535
  protocol          = "tcp"
  security_group_id = data.aws_security_group.eks_control_plane.id
  cidr_blocks       = [aws_subnet.weka_subnet1.cidr_block, aws_subnet.weka_subnet2.cidr_block]
}

output "endpoint" {
  value = aws_eks_cluster.eks.endpoint
}

output "kubeconfig-certificate-authority-data" {
  value = aws_eks_cluster.eks.certificate_authority.0.data
}

