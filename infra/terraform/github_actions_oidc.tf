# GitHub Actions OIDC deploy role (ECR push + ECS redeploy).
# Import existing resources before first apply:
#   terraform import aws_iam_role.github_actions_deploy github-actions-deploy-role
#   terraform import aws_iam_role_policy.github_actions_ecs_deploy github-actions-deploy-role:ECSDeployPolicy
#   terraform import aws_iam_role_policy_attachment.github_actions_ecr_power_user github-actions-deploy-role/arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryPowerUser

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

locals {
  github_oidc_subjects = concat(
    [
      "repo:elug3/schick-web:ref:refs/heads/master",
      "repo:elug3/schick-manage-web:ref:refs/heads/master",
      "repo:elug3/dupli1-web:ref:refs/heads/master",
      "repo:elug3/dupli1-manage-web:ref:refs/heads/master",
      "repo:elug3/dupli1:ref:refs/heads/main",
      "repo:elug3/dupli1:ref:refs/tags/v*",
    ],
    var.github_oidc_extra_subjects,
  )
}

resource "aws_iam_role" "github_actions_deploy" {
  name = "github-actions-deploy-role"
  description = "GitHub Actions OIDC role for Dupli1 CI (ECR push, ECS deploy)."

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect = "Allow"
      Principal = {
        Federated = data.aws_iam_openid_connect_provider.github.arn
      }
      Action = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
        }
        StringLike = {
          "token.actions.githubusercontent.com:sub" = local.github_oidc_subjects
        }
      }
    }]
  })
}

resource "aws_iam_role_policy" "github_actions_ecs_deploy" {
  name = "ECSDeployPolicy"
  role = aws_iam_role.github_actions_deploy.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "ECSDeploy"
        Effect = "Allow"
        Action = [
          "ecs:DescribeTaskDefinition",
          "ecs:RegisterTaskDefinition",
          "ecs:DescribeServices",
          "ecs:UpdateService",
          "ecs:DescribeClusters",
          "ecs:ListTasks",
          "ecs:DescribeTasks",
        ]
        Resource = "*"
      },
      {
        Sid      = "PassExecutionRole"
        Effect   = "Allow"
        Action   = "iam:PassRole"
        Resource = data.aws_iam_role.ecs_task_execution.arn
      },
    ]
  })
}

resource "aws_iam_role_policy_attachment" "github_actions_ecr_power_user" {
  role       = aws_iam_role.github_actions_deploy.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryPowerUser"
}

# PowerUser covers push/pull to existing repos, not CreateRepository.
# Needed by .github/workflows/aws.yml "Ensure ECR repository exists".
resource "aws_iam_role_policy" "github_actions_ecr_create" {
  name = "ECRCreateRepository"
  role = aws_iam_role.github_actions_deploy.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid    = "CreateDupli1ECRRepositories"
        Effect = "Allow"
        Action = [
          "ecr:CreateRepository",
          "ecr:TagResource",
          "ecr:PutImageScanningConfiguration",
        ]
        Resource = "*"
      },
    ]
  })
}
