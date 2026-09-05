# NATS client authorization token. Compose starts nats-server with --auth;
# every publisher/subscriber must present this token. ECS injects the same
# key from Secrets Manager dupli1/production/nats-token.

resource "random_password" "nats_token" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "nats" {
  name        = "${var.project_name}/${var.environment}/nats-token"
  description = "NATS --auth token for the Dupli1 event bus"

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}

resource "aws_secretsmanager_secret_version" "nats" {
  secret_id = aws_secretsmanager_secret.nats.id
  secret_string = jsonencode({
    NATS_TOKEN = random_password.nats_token.result
  })

  lifecycle {
    # Keep operator-rotated tokens; random_password only seeds the first version.
    ignore_changes = [secret_string]
  }
}
