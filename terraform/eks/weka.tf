module "weka" {
  source = "github.com/weka/terraform-aws-weka?ref=v1.0.4"

  prefix             = local.prefix
  cluster_name       = local.cluster_name
  availability_zones = ["us-east-2a"]
  allow_ssh_cidrs    = ["0.0.0.0/0"]
  get_weka_io_token  = var.get_weka_io_token
  clients_number     = 0

  vpc_id                   = aws_vpc.weka_vpc.id
  subnet_ids               = [aws_subnet.weka_subnet1.id]
  create_alb               = true
  alb_additional_subnet_id = aws_subnet.weka_subnet2.id

  sg_ids = [aws_security_group.eks_control_plane.id, aws_security_group.eks_worker_nodes.id]

  ssh_public_key = aws_key_pair.eks_key_pair.public_key
}
