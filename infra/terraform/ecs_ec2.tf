resource "aws_security_group" "ecs_instances" {
  name        = "${local.name_prefix}-ecs-instances"
  description = "ECS EC2 container instances"
  vpc_id      = var.vpc_id

  ingress {
    description     = "Allow awsvpc task ENI traffic within VPC"
    from_port       = 0
    to_port         = 0
    protocol        = "-1"
    cidr_blocks     = [data.aws_vpc.prod.cidr_block]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name        = "${local.name_prefix}-ecs-instances"
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_security_group" "ecs_tasks" {
  name        = "${local.name_prefix}-ecs-tasks"
  description = "Dupli1 ECS awsvpc tasks"
  vpc_id      = var.vpc_id

  ingress {
    description     = "HTTP from ALB to proxy"
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  ingress {
    description = "Inter-service traffic within VPC"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    self        = true
  }

  ingress {
    description = "Service discovery from ECS instances / tasks"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [data.aws_vpc.prod.cidr_block]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name        = "${local.name_prefix}-ecs-tasks"
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_security_group_rule" "rds_from_ecs_tasks" {
  type                     = "ingress"
  description              = "PostgreSQL from Dupli1 ECS tasks"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = var.rds_security_group_id
  source_security_group_id = aws_security_group.ecs_tasks.id
}

resource "aws_security_group_rule" "rds_from_ecs_instances" {
  type                     = "ingress"
  description              = "PostgreSQL from Dupli1 ECS container instances (ops / SSM)"
  from_port                = 5432
  to_port                  = 5432
  protocol                 = "tcp"
  security_group_id        = var.rds_security_group_id
  source_security_group_id = aws_security_group.ecs_instances.id
}

data "aws_iam_policy_document" "ecs_instance_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_instance" {
  name               = "${local.name_prefix}-ecs-instance"
  assume_role_policy = data.aws_iam_policy_document.ecs_instance_assume.json

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_iam_role_policy_attachment" "ecs_instance_ecs" {
  role       = aws_iam_role.ecs_instance.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role"
}

resource "aws_iam_role_policy_attachment" "ecs_instance_ssm" {
  role       = aws_iam_role.ecs_instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

# Instance role must opt itself into awsvpcTrunking before the ECS agent
# registers; otherwise each t3.large only places ~2 awsvpc tasks (no trunk ENI).
data "aws_iam_policy_document" "ecs_instance_trunking" {
  statement {
    sid = "OptInAwsvpcTrunking"
    actions = [
      "ecs:PutAccountSetting",
      "ecs:ListAccountSettings",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "ecs_instance_trunking" {
  name   = "${local.name_prefix}-ecs-instance-trunking"
  role   = aws_iam_role.ecs_instance.id
  policy = data.aws_iam_policy_document.ecs_instance_trunking.json
}

# Account-wide default (root principal). Complements per-role opt-in in user-data.
resource "aws_ecs_account_setting_default" "awsvpc_trunking" {
  name  = "awsvpcTrunking"
  value = "enabled"
}

resource "aws_iam_instance_profile" "ecs_instance" {
  name = "${local.name_prefix}-ecs-instance"
  role = aws_iam_role.ecs_instance.name
}

resource "aws_launch_template" "ecs" {
  name_prefix   = "${local.name_prefix}-ecs-"
  image_id      = local.ecs_ami_id
  instance_type = var.ecs_instance_type

  iam_instance_profile {
    name = aws_iam_instance_profile.ecs_instance.name
  }

  vpc_security_group_ids = [aws_security_group.ecs_instances.id]

  # Opt the instance role into trunking BEFORE writing ecs.config / agent start.
  # Requires aws CLI on the ECS-optimized AMI (Amazon Linux 2023).
  user_data = base64encode(<<-EOT
#!/bin/bash
set -euxo pipefail
TOKEN=$(curl -fsS -X PUT "http://169.254.169.254/latest/api/token" \
  -H "X-aws-ec2-metadata-token-ttl-seconds: 21600")
REGION=$(curl -fsS -H "X-aws-ec2-metadata-token: $TOKEN" \
  http://169.254.169.254/latest/meta-data/placement/region)
# Self-opt-in as the container instance role (no principal-arn = caller).
aws ecs put-account-setting \
  --name awsvpcTrunking \
  --value enabled \
  --region "$REGION"
cat > /etc/ecs/ecs.config <<EOF
ECS_CLUSTER=${var.ecs_cluster_name}
ECS_ENABLE_CONTAINER_METADATA=true
ECS_ENABLE_TASK_ENI=true
ECS_ENABLE_AWSVPC_TRUNKING=true
EOF
EOT
  )

  block_device_mappings {
    device_name = "/dev/xvda"

    ebs {
      volume_size           = 40
      volume_type           = "gp3"
      encrypted             = true
      delete_on_termination = true
    }
  }

  tag_specifications {
    resource_type = "instance"
    tags = {
      Name        = "${local.name_prefix}-ecs"
      Environment = var.environment
      Project     = var.project_name
    }
  }

  tags = {
    Name        = "${local.name_prefix}-ecs-lt"
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_autoscaling_group" "ecs" {
  name                = "${local.name_prefix}-ecs-asg"
  vpc_zone_identifier = var.private_subnet_ids
  desired_capacity    = var.ecs_asg_desired_capacity
  min_size            = var.ecs_asg_min_size
  max_size            = var.ecs_asg_max_size
  health_check_type   = "EC2"

  launch_template {
    id      = aws_launch_template.ecs.id
    version = "$Latest"
  }

  # Roll instances when the launch template changes (trunking user-data / AMI).
  instance_refresh {
    strategy = "Rolling"
    triggers = ["launch_template"]

    preferences {
      min_healthy_percentage = 50
      instance_warmup        = 120
    }
  }

  tag {
    key                 = "Name"
    value               = "${local.name_prefix}-ecs"
    propagate_at_launch = true
  }

  tag {
    key                 = "AmazonECSManaged"
    value               = "true"
    propagate_at_launch = true
  }

  depends_on = [
    aws_nat_gateway.prod,
    aws_route.private_default_nat,
    aws_iam_role_policy.ecs_instance_trunking,
    aws_ecs_account_setting_default.awsvpc_trunking,
  ]

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_ecs_capacity_provider" "ec2" {
  name = "${local.name_prefix}-ec2"

  auto_scaling_group_provider {
    auto_scaling_group_arn         = aws_autoscaling_group.ecs.arn
    managed_termination_protection = "DISABLED"

    managed_scaling {
      status                    = "ENABLED"
      target_capacity           = 100
      minimum_scaling_step_size = 1
      maximum_scaling_step_size = 1
    }
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_ecs_cluster_capacity_providers" "production" {
  cluster_name = data.aws_ecs_cluster.production.cluster_name

  capacity_providers = [aws_ecs_capacity_provider.ec2.name]

  default_capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }
}
