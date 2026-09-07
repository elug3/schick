data "aws_caller_identity" "current" {}

data "aws_vpc" "prod" {
  id = var.vpc_id
}

data "aws_subnet" "public" {
  count = length(var.public_subnet_ids)
  id    = var.public_subnet_ids[count.index]
}

data "aws_subnet" "private" {
  count = length(var.private_subnet_ids)
  id    = var.private_subnet_ids[count.index]
}

data "aws_ecs_cluster" "production" {
  cluster_name = var.ecs_cluster_name
}

data "aws_db_instance" "dupli1" {
  db_instance_identifier = var.rds_instance_identifier
}

data "aws_ecr_repository" "services" {
  for_each = toset([
    "dupli1-auth",
    "dupli1-product",
    "dupli1-order",
    "dupli1-cart",
    "dupli1-payment",
    "dupli1-notification",
    "dupli1-proxy",
  ])
  name = each.key
}

data "aws_ssm_parameter" "ecs_ami" {
  name = "/aws/service/ecs/optimized-ami/amazon-linux-2023/recommended/image_id"
}

data "aws_iam_role" "ecs_task_execution" {
  name = "ecsTaskExecutionRole"
}

data "aws_route_tables" "private" {
  vpc_id = var.vpc_id

  filter {
    name   = "tag:Name"
    values = ["*private*"]
  }
}

locals {
  account_id   = data.aws_caller_identity.current.account_id
  ecs_ami_id   = var.ecs_ami_id != "" ? var.ecs_ami_id : data.aws_ssm_parameter.ecs_ami.value
  name_prefix  = "${var.project_name}-${var.environment}"
  ecr_registry = "${local.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com"

  service_images = {
    auth         = "${data.aws_ecr_repository.services["dupli1-auth"].repository_url}:${var.image_tag}"
    product      = "${data.aws_ecr_repository.services["dupli1-product"].repository_url}:${var.image_tag}"
    order        = "${data.aws_ecr_repository.services["dupli1-order"].repository_url}:${var.image_tag}"
    cart         = "${data.aws_ecr_repository.services["dupli1-cart"].repository_url}:${var.image_tag}"
    payment      = "${data.aws_ecr_repository.services["dupli1-payment"].repository_url}:${var.image_tag}"
    notification = "${data.aws_ecr_repository.services["dupli1-notification"].repository_url}:${var.image_tag}"
    profile      = "${aws_ecr_repository.profile.repository_url}:${var.image_tag}"
    proxy        = "${data.aws_ecr_repository.services["dupli1-proxy"].repository_url}:${var.image_tag}"
  }
}
