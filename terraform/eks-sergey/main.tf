terraform {
  required_version = ">= 1.7.5"

  cloud {
    organization = "wekaio"
    workspaces {
      name = "eks-sergey"
    }
  }

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.43.0"
    }
    kubectl = {
      source  = "gavinbunney/kubectl"
      version = "1.14.0"
    }
  }
}

provider "aws" {
  region  = "eu-west-1"

  assume_role {
    role_arn = "arn:aws:iam::381492135989:role/Root"
  }
}

provider "kubernetes" {
  host                   = aws_eks_cluster.eks.endpoint
  cluster_ca_certificate = base64decode(aws_eks_cluster.eks.certificate_authority.0.data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", local.cluster_name]
  }
}

provider "kubectl" {}

provider "helm" {
  kubernetes {
    config_path = "~/.kube/config"
  }

  registry {
    url      = "oci://quay.io/weka.io/helm"
    username = "matthew_pfefferle_weka"
    password = "t/leZi8upBj5sxf9X9DY++HxEIU5D6XUQLr13m7dsDv9ArZhQmTuTrWPdsIsMGF3r5S03YqnK6Jd6LxANnv1dA=="
  }
}

data "aws_caller_identity" "current" {}
data "aws_availability_zones" "available" {}

locals {
  prefix             = "weka"
  cluster_name       = "sergey"
  kubernetes_version = "1.29"
}


resource "aws_vpc" "weka_vpc" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true

  tags = {
    Name = "${local.cluster_name}-vpc"
  }
}

resource "aws_subnet" "weka_subnet1" {
  vpc_id                  = aws_vpc.weka_vpc.id
  availability_zone       = data.aws_availability_zones.available.names[0]
  cidr_block              = "10.0.1.0/24"
  map_public_ip_on_launch = true

  tags = {
    Name                              = "weka_subnet1"
    "kubernetes.io/role/internal-elb" = "1"
  }
}

resource "aws_subnet" "weka_subnet2" {
  vpc_id                  = aws_vpc.weka_vpc.id
  availability_zone       = data.aws_availability_zones.available.names[1]
  cidr_block              = "10.0.2.0/24"
  map_public_ip_on_launch = true

  tags = {
    Name                              = "weka_subnet2"
    "kubernetes.io/role/internal-elb" = "1"
  }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.weka_vpc.id
  availability_zone       = data.aws_availability_zones.available.names[0]
  cidr_block              = "10.0.0.0/24"
  map_public_ip_on_launch = true

  tags = {
    Name = "weka_public_subnet"
  }
}

resource "aws_nat_gateway" "nat" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public.id
  depends_on    = [aws_internet_gateway.weka_igw]
}

resource "aws_eip" "nat" {
  domain = "vpc"
}

resource "aws_internet_gateway" "weka_igw" {
  vpc_id = aws_vpc.weka_vpc.id
}

resource "aws_route_table" "weka_route_table" {
  vpc_id = aws_vpc.weka_vpc.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.weka_igw.id
  }
}

resource "aws_route_table_association" "weka_subnet1_association" {
  subnet_id      = aws_subnet.weka_subnet1.id
  route_table_id = aws_route_table.weka_route_table.id
}

resource "aws_route_table_association" "weka_subnet2_association" {
  subnet_id      = aws_subnet.weka_subnet2.id
  route_table_id = aws_route_table.weka_route_table.id
}
