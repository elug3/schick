# Pre-existing service repos (auth, product, …) are looked up in data.tf.
# dupli1-profile is new and was never created in the registry; GitHub Actions
# AmazonEC2ContainerRegistryPowerUser cannot ecr:CreateRepository.

resource "aws_ecr_repository" "profile" {
  name                 = "dupli1-profile"
  image_tag_mutability = "MUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }

  tags = {
    Environment = var.environment
    Project     = var.project_name
  }
}
