# Cloud Map registrations for services that did not already have them.
resource "aws_service_discovery_service" "order" {
  name = "order"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "cart" {
  name = "cart"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "payment" {
  name = "payment"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "redis" {
  name = "redis"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "nats" {
  name = "nats"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "notification" {
  name = "notification"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_service_discovery_service" "profile" {
  name = "profile"

  dns_config {
    namespace_id = var.service_discovery_namespace_id

    dns_records {
      ttl  = 10
      type = "A"
    }

    routing_policy = "MULTIVALUE"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

data "aws_service_discovery_service" "auth" {
  name         = "auth"
  namespace_id = var.service_discovery_namespace_id
}

data "aws_service_discovery_service" "product" {
  name         = "product"
  namespace_id = var.service_discovery_namespace_id
}

data "aws_service_discovery_service" "proxy" {
  name         = "proxy"
  namespace_id = var.service_discovery_namespace_id
}

locals {
  common_task = {
    network_mode             = "awsvpc"
    requires_compatibilities = ["EC2"]
    execution_role_arn       = data.aws_iam_role.ecs_task_execution.arn
  }
  nats_token_secret = {
    name      = "NATS_TOKEN"
    valueFrom = "${aws_secretsmanager_secret.nats.arn}:NATS_TOKEN::"
  }
}

resource "aws_ecs_task_definition" "redis" {
  family                   = "${var.project_name}-redis"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "128"
  memory                   = "256"

  container_definitions = jsonencode([
    {
      name      = "redis"
      image     = "redis:7-alpine"
      essential = true
      portMappings = [
        {
          containerPort = 6379
          hostPort      = 6379
          protocol      = "tcp"
        }
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["redis"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "nats" {
  family                   = "${var.project_name}-nats"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "128"
  memory                   = "256"

  container_definitions = jsonencode([
    {
      name      = "nats"
      image     = "nats:2-alpine"
      essential = true
      # Official image ENTRYPOINT is nats-server; wrap so --auth comes from
      # Secrets Manager instead of a plaintext task-definition argument.
      entrypoint = ["/bin/sh", "-c"]
      command    = ["exec nats-server -js --auth \"$NATS_TOKEN\""]
      portMappings = [
        {
          containerPort = 4222
          hostPort      = 4222
          protocol      = "tcp"
        }
      ]
      secrets = [
        local.nats_token_secret,
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["nats"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "auth" {
  family                   = "${var.project_name}-auth"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "auth"
      image     = local.service_images.auth
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "DUPLI1_AUTH_ADDR", value = ":8080" },
        { name = "REDIS_URL", value = "redis://redis.dupli1.local:6379" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
        # TEMPORARY: public customer signup without user.create. Set false to re-lock.
        { name = "AUTH_OPEN_REGISTER", value = "true" },
      ]
      secrets = concat([
        {
          name      = "DB_URL"
          valueFrom = var.auth_db_url_secret_arn
        },
        {
          name      = "JWT_SECRET"
          valueFrom = var.jwt_secret_arn
        },
        {
          name      = "DUPLI1_WEB_SERVICE_EMAIL"
          valueFrom = "${aws_secretsmanager_secret.web_service.arn}:DUPLI1_WEB_SERVICE_EMAIL::"
        },
        {
          name      = "DUPLI1_WEB_SERVICE_PASSWORD"
          valueFrom = "${aws_secretsmanager_secret.web_service.arn}:DUPLI1_WEB_SERVICE_PASSWORD::"
        },
        {
          name      = "DUPLI1_ORDER_SERVICE_EMAIL"
          valueFrom = "${aws_secretsmanager_secret.order_service.arn}:DUPLI1_ORDER_SERVICE_EMAIL::"
        },
        {
          name      = "DUPLI1_ORDER_SERVICE_PASSWORD"
          valueFrom = "${aws_secretsmanager_secret.order_service.arn}:DUPLI1_ORDER_SERVICE_PASSWORD::"
        },
        ],
        # Without this, auth signs with an ephemeral key that changes on every restart.
        var.jwt_private_key_secret_arn == "" ? [] : [
          {
            name      = "JWT_PRIVATE_KEY"
            valueFrom = var.jwt_private_key_secret_arn
          },
        ],
        [local.nats_token_secret]
      )
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["auth"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "product" {
  family                   = "${var.project_name}-product"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "product"
      image     = local.service_images.product
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "SERVER_HOST", value = "0.0.0.0" },
        { name = "SERVER_PORT", value = "8080" },
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
        { name = "S3_ENDPOINT", value = "https://s3.${var.aws_region}.amazonaws.com" },
        { name = "S3_PUBLIC_ENDPOINT", value = local.product_images_public_base },
        { name = "S3_BUCKET", value = aws_s3_bucket.product_images.id },
        { name = "GUEST_COOKIE_SECURE", value = "true" },
      ]
      secrets = [
        {
          name      = "DUPLI1_PRODUCT_DB"
          valueFrom = var.product_db_url_secret_arn
        },
        {
          name      = "JWT_SECRET"
          valueFrom = var.jwt_secret_arn
        },
        {
          name      = "S3_ACCESS_KEY"
          valueFrom = "${aws_secretsmanager_secret.product_s3.arn}:S3_ACCESS_KEY::"
        },
        {
          name      = "S3_SECRET_KEY"
          valueFrom = "${aws_secretsmanager_secret.product_s3.arn}:S3_SECRET_KEY::"
        },
        local.nats_token_secret,
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["product"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "order" {
  family                   = "${var.project_name}-order"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "order"
      image     = local.service_images.order
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "DUPLI1_ORDER_ADDR", value = ":8080" },
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
        { name = "DUPLI1_GATEWAY_URL", value = "http://proxy.dupli1.local" },
        { name = "DUPLI1_AUTH_URL", value = "http://auth.dupli1.local:8080" },
      ]
      secrets = [
        {
          name      = "DUPLI1_ORDER_DB"
          valueFrom = var.order_db_url_secret_arn
        },
        {
          name      = "JWT_SECRET"
          valueFrom = var.jwt_secret_arn
        },
        {
          name      = "DUPLI1_ORDER_SERVICE_EMAIL"
          valueFrom = "${aws_secretsmanager_secret.order_service.arn}:DUPLI1_ORDER_SERVICE_EMAIL::"
        },
        {
          name      = "DUPLI1_ORDER_SERVICE_PASSWORD"
          valueFrom = "${aws_secretsmanager_secret.order_service.arn}:DUPLI1_ORDER_SERVICE_PASSWORD::"
        },
        local.nats_token_secret,
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["order"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "cart" {
  family                   = "${var.project_name}-cart"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "cart"
      image     = local.service_images.cart
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "DUPLI1_CART_ADDR", value = ":8080" },
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
        { name = "DUPLI1_PRODUCT_URL", value = "http://product.dupli1.local:8080" },
        { name = "DUPLI1_INVENTORY_URL", value = "http://product.dupli1.local:8080" },
      ]
      secrets = [
        {
          name      = "DUPLI1_CART_DB"
          valueFrom = var.cart_db_url_secret_arn
        },
        {
          name      = "JWT_SECRET"
          valueFrom = var.jwt_secret_arn
        },
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["cart"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "profile" {
  family                   = "${var.project_name}-profile"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "profile"
      image     = local.service_images.profile
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "DUPLI1_PROFILE_ADDR", value = ":8080" },
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
      ]
      # NOTE: DUPLI1_PROFILE_DB is only injected once profile_db_url_secret_arn
      # is set (create dupli1/production/profile-db-url in Secrets Manager and
      # pass the ARN via var.profile_db_url_secret_arn). Until then the task
      # starts with no secrets block for the DB and profile falls back to its
      # in-memory repository, which does not persist across restarts.
      secrets = concat(
        var.profile_db_url_secret_arn == "" ? [] : [
          {
            name      = "DUPLI1_PROFILE_DB"
            valueFrom = var.profile_db_url_secret_arn
          },
        ],
        [
          {
            name      = "JWT_SECRET"
            valueFrom = var.jwt_secret_arn
          },
          local.nats_token_secret,
        ]
      )
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["profile"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "payment" {
  family                   = "${var.project_name}-payment"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "256"
  memory                   = "512"

  container_definitions = jsonencode([
    {
      name      = "payment"
      image     = local.service_images.payment
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        { name = "DUPLI1_PAYMENT_ADDR", value = ":8080" },
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
        { name = "DUPLI1_ORDER_URL", value = "http://order.dupli1.local:8080" },
        { name = "DUPLI1_PAYMENT_PUBLIC_URL", value = "https://dupli1.com" },
        { name = "NANO_BASE_URL", value = var.nano_base_url },
        { name = "NANO_SUCCESS_URL", value = "https://dupli1.com/checkout/confirmation" },
        { name = "NANO_FAILURE_URL", value = "https://dupli1.com/checkout" },
      ]
      secrets = [
        {
          name      = "DUPLI1_PAYMENT_DB"
          valueFrom = var.payment_db_url_secret_arn
        },
        {
          name      = "JWT_SECRET"
          valueFrom = var.jwt_secret_arn
        },
        {
          name      = "NANO_API_KEY"
          valueFrom = "${aws_secretsmanager_secret.nano_payment.arn}:NANO_API_KEY::"
        },
        {
          name      = "NANO_LOGIN_ID"
          valueFrom = "${aws_secretsmanager_secret.nano_payment.arn}:NANO_LOGIN_ID::"
        },
        {
          name      = "NANO_SHOPCODE"
          valueFrom = "${aws_secretsmanager_secret.nano_payment.arn}:NANO_SHOPCODE::"
        },
        {
          name      = "NANO_VER"
          valueFrom = "${aws_secretsmanager_secret.nano_payment.arn}:NANO_VER::"
        },
        local.nats_token_secret,
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["payment"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}
resource "aws_ecs_task_definition" "notification" {
  family                   = "${var.project_name}-notification"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "128"
  memory                   = "256"

  container_definitions = jsonencode([
    {
      name      = "notification"
      image     = local.service_images.notification
      essential = true
      portMappings = [
        {
          containerPort = 8080
          hostPort      = 8080
          protocol      = "tcp"
        }
      ]
      environment = [
        # Match nginx upstream dupli1-notification:8080 (default process port is 8084).
        { name = "DUPLI1_NOTIFICATION_ADDR", value = ":8080" },
        { name = "NATS_URL", value = "nats://nats.dupli1.local:4222" },
        { name = "MANAGE_WEB_URL", value = "https://manage.dupli1.com" },
        # Required for manage-web /telegram (manager JWT via auth JWKS).
        { name = "AUTH_JWKS_URL", value = "http://auth.dupli1.local:8080/api/v1/auth/.well-known/jwks.json" },
      ]
      secrets = [
        {
          name      = "TELEGRAM_BOT_TOKEN"
          valueFrom = "${var.telegram_secret_arn}:TELEGRAM_BOT_TOKEN::"
        },
        {
          name      = "TELEGRAM_ORDER_CHAT_ID"
          valueFrom = "${var.telegram_secret_arn}:TELEGRAM_ORDER_CHAT_ID::"
        },
        {
          name      = "TELEGRAM_PRODUCT_CHAT_ID"
          valueFrom = "${var.telegram_secret_arn}:TELEGRAM_PRODUCT_CHAT_ID::"
        },
        {
          name      = "TELEGRAM_ALLOWED_USER_IDS"
          valueFrom = "${var.telegram_secret_arn}:TELEGRAM_ALLOWED_USER_IDS::"
        },
        local.nats_token_secret,
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["notification"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "proxy" {
  family                   = "${var.project_name}-proxy"
  network_mode             = local.common_task.network_mode
  requires_compatibilities = local.common_task.requires_compatibilities
  execution_role_arn       = local.common_task.execution_role_arn
  cpu                      = "128"
  memory                   = "256"

  container_definitions = jsonencode([
    {
      name      = "proxy"
      image     = local.service_images.proxy
      essential = true
      portMappings = [
        {
          containerPort = 80
          hostPort      = 80
          protocol      = "tcp"
        }
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.services["proxy"].name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

locals {
  capacity_provider_strategy = [
    {
      capacity_provider = aws_ecs_capacity_provider.ec2.name
      weight            = 1
      base              = 1
    }
  ]

  private_network = {
    subnets         = var.private_subnet_ids
    security_groups = [aws_security_group.ecs_tasks.id]
  }
}

resource "aws_ecs_service" "redis" {
  name            = "dupli1-redis"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.redis.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.redis.arn
  }

  depends_on = [
    aws_ecs_cluster_capacity_providers.production,
    aws_nat_gateway.prod,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "nats" {
  name            = "dupli1-nats"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.nats.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.nats.arn
  }

  depends_on = [
    aws_ecs_cluster_capacity_providers.production,
    aws_nat_gateway.prod,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "auth" {
  name            = "dupli1-auth"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.auth.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = data.aws_service_discovery_service.auth.arn
  }

  depends_on = [
    aws_ecs_service.redis,
    aws_ecs_service.nats,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "product" {
  name            = "dupli1-product"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.product.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = data.aws_service_discovery_service.product.arn
  }

  depends_on = [
    aws_ecs_service.auth,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "order" {
  name            = "dupli1-order"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.order.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.order.arn
  }

  depends_on = [
    aws_ecs_service.auth,
    aws_ecs_service.product,
    aws_ecs_service.nats,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "cart" {
  name            = "dupli1-cart"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.cart.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.cart.arn
  }

  depends_on = [
    aws_ecs_service.auth,
    aws_ecs_service.product,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "profile" {
  name            = "dupli1-profile"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.profile.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.profile.arn
  }

  depends_on = [
    aws_ecs_service.auth,
    aws_ecs_service.nats,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "payment" {
  name            = "dupli1-payment"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.payment.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.payment.arn
  }

  depends_on = [
    aws_ecs_service.order,
    aws_ecs_service.nats,
    aws_iam_role_policy.ecs_execution_secrets,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "notification" {
  name            = "dupli1-notification"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.notification.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  service_registries {
    registry_arn = aws_service_discovery_service.notification.arn
  }

  depends_on = [aws_ecs_service.nats]

  lifecycle {
    ignore_changes = [desired_count]
  }
}

resource "aws_ecs_service" "proxy" {
  name            = "dupli1-proxy"
  cluster         = data.aws_ecs_cluster.production.id
  task_definition = aws_ecs_task_definition.proxy.arn
  desired_count   = var.desired_count

  capacity_provider_strategy {
    capacity_provider = aws_ecs_capacity_provider.ec2.name
    weight            = 1
    base              = 1
  }

  network_configuration {
    subnets         = local.private_network.subnets
    security_groups = local.private_network.security_groups
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.proxy.arn
    container_name   = "proxy"
    container_port   = 80
  }

  service_registries {
    registry_arn = data.aws_service_discovery_service.proxy.arn
  }

  depends_on = [
    aws_lb_listener.http,
    aws_lb_listener.https,
    aws_ecs_service.auth,
    aws_ecs_service.product,
    aws_ecs_service.order,
    aws_ecs_service.cart,
    aws_ecs_service.payment,
  ]

  lifecycle {
    ignore_changes = [desired_count]
  }
}
