output "vpc_id" {
  value = aws_vpc.weka_vpc.id
}

output "subnet_ids" {
  value = [aws_subnet.weka_subnet1.id, aws_subnet.weka_subnet2.id]
}
