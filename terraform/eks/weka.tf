module "weka" {
  source = "github.com/weka/terraform-aws-weka?ref=v1.0.4"

  prefix             = local.prefix
  cluster_name       = local.cluster_name
  availability_zones = ["us-east-2a"]
  allow_ssh_cidrs    = ["0.0.0.0/0"]
  get_weka_io_token  = var.get_weka_io_token
  clients_number     = 0
  install_weka_url   = "https://EZxlx8iydgqQTcvZ@get.prod.weka.io/dist/v1/install/4.2.9-406c00f199f95e0399f1a52f4474adb6/4.2.9.214-27fc8e5a8889d57c9ebdf0768e098c5a"

  vpc_id                   = aws_vpc.weka_vpc.id
  subnet_ids               = [aws_subnet.weka_subnet1.id]
  create_alb               = true
  alb_additional_subnet_id = aws_subnet.weka_subnet2.id

  sg_ids = [data.aws_security_group.eks_control_plane.id, aws_security_group.eks_worker_nodes.id, aws_eks_cluster.eks.vpc_config[0].cluster_security_group_id]

  ssh_public_key = aws_key_pair.eks_key_pair.public_key
}

