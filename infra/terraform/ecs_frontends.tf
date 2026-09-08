# Public storefront (dupli1-web) and public admin (manage.dupli1.com → dupli1-manage-web).

data "aws_ecr_repository" "web" {
  name = "web"
}

data "aws_ecr_repository" "manage_web" {
  name = "manage-web"
}

data "aws_service_discovery_service" "manage" {
  name         = "manage"
  namespace_id = var.service_discovery_namespace_id
}

resource "aws_cloudwatch_log_group" "web" {
  name              = "/ecs/${var.project_name}-web"
  retention_in_days = 14

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_cloudwatch_log_group" "manage_web" {
  name              = "/ecs/${var.project_name}-manage-web"
  retention_in_days = 14

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

# Instance TG — bridge-mode tasks publish host port 3000 (avoids awsvpc ENI limits).
resource "aws_lb_target_group" "web" {
  name        = "${local.name_prefix}-web-tg"
  port        = 3000
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "instance"

  health_check {
    enabled             = true
    path                = "/"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 15
    matcher             = "200-399"
  }

  tags = {
    Name        = "${local.name_prefix}-web-tg"
    Environment = var.environment
    Project     = var.project_name
  }
}

# Admin UI — bridge-mode like storefront (avoids one awsvpc ENI per host).
# Host port 80 is free for bridge tasks: proxy binds :80 on its task ENI, not the host.
# Name suffix -inst marks instance target_type (replaces former ip-mode manage-tg).
resource "aws_lb_target_group" "manage_web" {
  name        = "${local.name_prefix}-manage-inst-tg"
  port        = 80
  protocol    = "HTTP"
  vpc_id      = var.vpc_id
  target_type = "instance"

  health_check {
    enabled             = true
    path                = "/"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 15
    matcher             = "200-399"
  }

  tags = {
    Name        = "${local.name_prefix}-manage-inst-tg"
    Environment = var.environment
    Project     = var.project_name
  }
}

# API + gateway stay on proxy; default HTTPS listener action forwards to web.
resource "aws_lb_listener_rule" "api" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 10

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.proxy.arn
  }

  condition {
    path_pattern {
      values = ["/api/*", "/gateway/*"]
    }
  }
}

# Keep HTTP API/gateway working during clients that skip the redirect (health checks).
resource "aws_lb_listener_rule" "api_http" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 10

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.proxy.arn
  }

  condition {
    path_pattern {
      values = ["/api/*", "/gateway/*"]
    }
  }
}

resource "aws_lb_listener_rule" "manage_https" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 20

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.manage_web.arn
  }

  condition {
    host_header {
      values = ["manage.dupli1.com"]
    }
  }
}

resource "aws_lb_listener_rule" "manage_http" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 20

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.manage_web.arn
  }

  condition {
    host_header {
      values = ["manage.dupli1.com"]
    }
  }
}

resource "aws_route53_record" "manage" {
  zone_id = var.route53_zone_id
  name    = "manage.dupli1.com"
  type    = "A"

  alias {
    name                   = aws_lb.prod.dns_name
    zone_id                = aws_lb.prod.zone_id
    evaluate_target_health = true
  }
}

resource "aws_security_group_rule" "alb_to_web_host" {
  type                     = "ingress"
  description              = "Storefront host port from ALB"
  from_port                = 3000
  to_port                  = 3000
  protocol                 = "tcp"
  security_group_id        = aws_security_group.ecs_instances.id
  source_security_group_id = aws_security_group.alb.id
}

resource "aws_security_group_rule" "alb_to_manage_host" {
  type                     = "ingress"
  description              = "Admin UI host port from ALB"
  from_port                = 80
  to_port                  = 80
  protocol                 = "tcp"
  security_group_id        = aws_security_group.ecs_instances.id
  source_security_group_id = aws_security_group.alb.id
}

resource "aws_ecs_task_definition" "web" {
  family                   = "${var.project_name}-web"
  network_mode             = "bridge"
  requires_compatibilities = ["EC2"]
  execution_role_arn       = data.aws_iam_role.ecs_task_execution.arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "web"
      image     = "${data.aws_ecr_repository.web.repository_url}:${var.image_tag}"
      essential = true
      memory    = 512
      cpu       = 256
      portMappings = [
        {
          containerPort = 3000
          hostPort      = 3000
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "PORT", value = "3000" },
        { name = "HOST", value = "0.0.0.0" },
        { name = "DUPLI1_API_BASE_URL", value = "http://proxy.dupli1.local" },
      ]
      secrets = [
        {
          name      = "DUPLI1_WEB_SERVICE_EMAIL"
          valueFrom = "${aws_secretsmanager_secret.web_service.arn}:DUPLI1_WEB_SERVICE_EMAIL::"
        },
        {
          name      = "DUPLI1_WEB_SERVICE_PASSWORD"
          valueFrom = "${aws_secretsmanager_secret.web_service.arn}:DUPLI1_WEB_SERVICE_PASSWORD::"
        },
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.web.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "manage_web" {
  family                   = "${var.project_name}-manage-web"
  network_mode             = "bridge"
  requires_compatibilities = ["EC2"]
  execution_role_arn       = data.aws_iam_role.ecs_task_execution.arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "manage-web"
      image     = "${data.aws_ecr_repository.manage_web.repository_url}:${var.image_tag}"
      essential = true
      memory    = 512
      cpu       = 256
      portMappings = [
        {
          containerPort = 80
          hostPort      = 80
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "PORT", value = "80" },
        { name = "HOST", value = "0.0.0.0" },
        { name = "DUPLI1_GATEWAY_URL", value = "http://proxy.dupli1.local" },
        { name = "DUPLI1_API_BASE_URL", value = "http://proxy.dupli1.local" },
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.manage_web.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

# Public storefront — ALB default target (bridge mode, no awsvpc ENI).
resource "aws_ecs_service" "web" {
  name            = "dupli1-web"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.web.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.web.arn
    container_name   = "web"
    container_port   = 3000
  }

  depends_on = [
    aws_lb_listener_rule.api,
    aws_lb_listener_rule.api_http,
    aws_lb_listener.http,
    aws_lb_listener.https,
    aws_ecs_cluster_capacity_providers.production,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

# Admin UI — bridge mode (host :80) so it does not consume an awsvpc ENI.
resource "aws_ecs_service" "manage_web" {
  name            = "dupli1-manage-web"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.manage_web.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.manage_web.arn
    container_name   = "manage-web"
    container_port   = 80
  }

  # Bridge + Cloud Map A record registers the EC2 primary IP (host :80).
  service_registries {
    registry_arn = data.aws_service_discovery_service.manage.arn
  }

  depends_on = [
    aws_ecs_service.proxy,
    aws_lb_listener_rule.manage_https,
    aws_lb_listener_rule.manage_http,
    aws_ecs_cluster_capacity_providers.production,
    aws_security_group_rule.alb_to_manage_host,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}
