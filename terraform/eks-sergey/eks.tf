resource "aws_key_pair" "eks_key_pair" {
  key_name   = "${local.prefix}-${local.cluster_name}-eks-ssh-key"
  public_key = file("~/.ssh/weka_dev_ssh_key.pub")
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


output "endpoint" {
  value = aws_eks_cluster.eks.endpoint
}

output "kubeconfig-certificate-authority-data" {
  value = aws_eks_cluster.eks.certificate_authority.0.data
}

