output "alb_dns_name" {
  description = "Public DNS name of the Application Load Balancer."
  value       = aws_lb.prod.dns_name
}

output "alb_url" {
  description = "HTTPS URL for the public site (ALB)."
  value       = "https://${aws_lb.prod.dns_name}"
}

output "nat_gateway_id" {
  description = "NAT Gateway used by private ECS tasks."
  value       = aws_nat_gateway.prod.id
}

output "ecs_capacity_provider" {
  description = "ECS EC2 capacity provider name."
  value       = aws_ecs_capacity_provider.ec2.name
}

output "ecs_asg_name" {
  description = "Auto Scaling group for ECS container instances."
  value       = aws_autoscaling_group.ecs.name
}

output "product_images_bucket" {
  description = "S3 bucket for product images."
  value       = aws_s3_bucket.product_images.id
}

output "product_images_cdn_domain" {
  description = "CloudFront domain name for product images."
  value       = aws_cloudfront_distribution.product_images.domain_name
}

output "product_images_cdn_url" {
  description = "Public HTTPS base for product image objects (custom alias or CloudFront domain). Set as S3_PUBLIC_ENDPOINT."
  value       = local.product_images_public_base
}

output "product_images_public_base" {
  description = "Deprecated alias of product_images_cdn_url."
  value       = local.product_images_public_base
}

output "rds_endpoint" {
  description = "Existing RDS hostname."
  value       = data.aws_db_instance.dupli1.address
}

output "ecs_tasks_security_group_id" {
  description = "Security group attached to ECS tasks."
  value       = aws_security_group.ecs_tasks.id
}

output "gateway_health_url" {
  description = "ALB gateway health check URL."
  value       = "https://${aws_lb.prod.dns_name}/gateway/health"
}

output "storefront_note" {
  description = "Public storefront is served at ALB / (dupli1-web)."
  value       = "https://dupli1.com/"
}

output "manage_web_url" {
  description = "Public admin UI URL (ALB host-header manage.dupli1.com)."
  value       = "https://manage.dupli1.com"
}

output "github_actions_deploy_role_arn" {
  description = "IAM role ARN for GitHub Actions OIDC (ECR + ECS deploy)."
  value       = aws_iam_role.github_actions_deploy.arn
}

output "profile_ecr_repository_url" {
  description = "ECR repository URL for dupli1-profile (Terraform-managed)."
  value       = aws_ecr_repository.profile.repository_url
}
