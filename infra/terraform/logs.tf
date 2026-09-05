resource "aws_cloudwatch_log_group" "services" {
  for_each = toset([
    "auth",
    "product",
    "order",
    "cart",
    "payment",
    "notification",
    "profile",
    "proxy",
    "redis",
    "nats",
  ])

  name              = "/ecs/${var.project_name}-${each.key}"
  retention_in_days = 14

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

data "aws_iam_policy_document" "ecs_execution_secrets" {
  statement {
    sid    = "ReadDupli1Secrets"
    effect = "Allow"
    actions = [
      "secretsmanager:GetSecretValue",
    ]
    resources = compact([
      var.auth_db_url_secret_arn,
      var.product_db_url_secret_arn,
      var.order_db_url_secret_arn,
      var.cart_db_url_secret_arn,
      var.payment_db_url_secret_arn,
      var.profile_db_url_secret_arn,
      var.jwt_secret_arn,
      var.jwt_private_key_secret_arn,
      var.telegram_secret_arn,
      aws_secretsmanager_secret.product_s3.arn,
      aws_secretsmanager_secret.web_service.arn,
      aws_secretsmanager_secret.order_service.arn,
      aws_secretsmanager_secret.nano_payment.arn,
      aws_secretsmanager_secret.nats.arn,
    ])
  }
}

resource "aws_iam_role_policy" "ecs_execution_secrets" {
  name   = "${local.name_prefix}-ecs-execution-secrets"
  role   = data.aws_iam_role.ecs_task_execution.name
  policy = data.aws_iam_policy_document.ecs_execution_secrets.json
}
