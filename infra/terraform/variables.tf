variable "aws_region" {
  description = "AWS region for Dupli1 resources."
  type        = string
  default     = "us-east-1"
}

variable "project_name" {
  description = "Resource name prefix."
  type        = string
  default     = "dupli1"
}

variable "environment" {
  description = "Deployment environment label."
  type        = string
  default     = "production"
}

variable "vpc_id" {
  description = "Existing VPC that hosts ECS and RDS."
  type        = string
  default     = "vpc-0e143b53ca2a4714c"
}

variable "public_subnet_ids" {
  description = "Public subnets for ALB and NAT."
  type        = list(string)
  default = [
    "subnet-0d757c4cf8d71963b",
    "subnet-02c1003124987322c",
  ]
}

variable "private_subnet_ids" {
  description = "Private subnets for ECS tasks and EC2 container instances."
  type        = list(string)
  default = [
    "subnet-01fd0882721f10499",
    "subnet-006b8428713711816",
  ]
}

variable "ecs_cluster_name" {
  description = "Existing ECS cluster name."
  type        = string
  default     = "production"
}

variable "service_discovery_namespace_id" {
  description = "Cloud Map private DNS namespace ID (dupli1.local)."
  type        = string
  default     = "ns-5d53uocv3zvhmgrz"
}

variable "rds_security_group_id" {
  description = "Security group attached to the existing RDS instance."
  type        = string
  default     = "sg-073c0d32fa81e03e6"
}

variable "rds_instance_identifier" {
  description = "Existing RDS instance identifier."
  type        = string
  default     = "dupli1-production"
}

variable "auth_db_url_secret_arn" {
  description = "Secrets Manager ARN for auth DB_URL."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/auth-db-url-B6TnOD"
}

variable "product_db_url_secret_arn" {
  description = "Secrets Manager ARN for product DUPLI1_PRODUCT_DB."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/product-db-url-kaL4uk"
}

variable "order_db_url_secret_arn" {
  description = "Secrets Manager ARN for order DUPLI1_ORDER_DB."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/order-db-url-RAI64m"
}

variable "cart_db_url_secret_arn" {
  description = "Secrets Manager ARN for cart DUPLI1_CART_DB."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/cart-db-url-GSYbXs"
}

variable "payment_db_url_secret_arn" {
  description = "Secrets Manager ARN for payment DUPLI1_PAYMENT_DB."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/payments-db-url-XxgHJp"
}

variable "profile_db_url_secret_arn" {
  description = "Secrets Manager ARN for profile DUPLI1_PROFILE_DB. Not yet provisioned in Secrets Manager — create dupli1/production/profile-db-url before applying, then set this default (or pass via tfvars)."
  type        = string
  default     = ""
}

variable "jwt_secret_arn" {
  description = "Secrets Manager ARN for JWT_SECRET (HS256 fallback)."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/jwt-secret-tTYcMy"
}

variable "jwt_private_key_secret_arn" {
  description = <<-EOT
    Secrets Manager ARN holding the PEM-encoded RSA private key auth signs RS256 tokens
    with (injected as JWT_PRIVATE_KEY). Leave empty and auth generates a throwaway key on
    every start, which invalidates every issued token and breaks JWKS validation in the
    other services after a deploy. See docs/v1-release-plan.md for the rollout steps.
  EOT
  type        = string
  default     = ""
}

variable "telegram_secret_arn" {
  description = "Secrets Manager ARN for Telegram bot JSON (TELEGRAM_BOT_TOKEN, TELEGRAM_ORDER_CHAT_ID, TELEGRAM_PRODUCT_CHAT_ID, TELEGRAM_ALLOWED_USER_IDS)."
  type        = string
  default     = "arn:aws:secretsmanager:us-east-1:845061289093:secret:dupli1/production/telegram-G9Oskq"
}

variable "acm_certificate_arn" {
  description = "ACM certificate ARN for HTTPS on the ALB (dupli1.com)."
  type        = string
  default     = "arn:aws:acm:us-east-1:845061289093:certificate/a5e612a6-8bec-4d02-8f98-cc8484aa2fc1"
}

variable "product_images_cdn_aliases" {
  description = "Custom domain aliases for the product-images CloudFront distribution (e.g. images.dupli1.com). Empty uses the default *.cloudfront.net domain."
  type        = list(string)
  default     = ["images.dupli1.com"]
}

variable "product_images_cdn_certificate_arn" {
  description = "ACM certificate ARN for product-images CDN aliases (must be in us-east-1). Defaults to acm_certificate_arn."
  type        = string
  default     = ""
}

variable "product_images_cdn_price_class" {
  description = "CloudFront price class for product images (PriceClass_100 = NA/EU, cheaper)."
  type        = string
  default     = "PriceClass_100"
}

variable "route53_zone_id" {
  description = "Public Route53 hosted zone ID for dupli1.com."
  type        = string
  default     = "Z04998762RV4NUS16WWXV"
}

variable "public_dns_names" {
  description = "Public DNS names that should alias to the ALB."
  type        = list(string)
  default     = ["dupli1.com", "www.dupli1.com"]
}

variable "ecs_ami_id" {
  description = "ECS-optimized AMI. Empty = latest Amazon Linux 2023 from SSM."
  type        = string
  default     = ""
}

variable "ecs_instance_type" {
  description = "EC2 instance type for the ECS capacity provider ASG."
  type        = string
  default     = "t3.large"
}

variable "ecs_asg_desired_capacity" {
  description = "Desired ECS hosts. With instance-role awsvpcTrunking (user-data opt-in) and manage-web on bridge, 2×t3.large packs all services. Without trunking, raise to ~5."
  type        = number
  default     = 2
}

variable "ecs_asg_min_size" {
  description = "Minimum number of ECS container instances."
  type        = number
  default     = 1
}

variable "ecs_asg_max_size" {
  description = "Maximum number of ECS container instances."
  type        = number
  default     = 4
}

variable "image_tag" {
  description = "ECR image tag for backend services."
  type        = string
  default     = "latest"
}

variable "desired_count" {
  description = "Desired task count per application service."
  type        = number
  default     = 1
}

variable "web_service_email" {
  description = "Email for the dupli1-web service account (customer registration)."
  type        = string
  default     = "dupli1-web@web.dupli1.com"
}

variable "nano_base_url" {
  description = "NANO PG base URL. Production: https://pay.nanopay.co.kr. Use https://dev3.nanopay.co.kr only with NANO 연동 테스트 credentials."
  type        = string
  default     = "https://pay.nanopay.co.kr"
}

variable "github_oidc_extra_subjects" {
  description = "Additional token.actions.githubusercontent.com:sub values for the GitHub Actions deploy role."
  type        = list(string)
  default     = []
}
