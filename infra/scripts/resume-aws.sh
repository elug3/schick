#!/usr/bin/env bash
# Resume Dupli1 production AWS compute after pause-aws.sh.
#
# Starts RDS, restores ECS ASG capacity, then scales backend services to 1.
#
# If pause used DELETE_NAT=1, recreate NAT first:
#   APPLY_NAT=1 bash infra/scripts/resume-aws.sh
#
# Usage:
#   bash infra/scripts/resume-aws.sh
#   APPLY_NAT=1 bash infra/scripts/resume-aws.sh
#   DESIRED_COUNT=1 ASG_DESIRED=1 bash infra/scripts/resume-aws.sh
set -euo pipefail

AWS_REGION="${AWS_REGION:-us-east-1}"
CLUSTER="${ECS_CLUSTER:-production}"
RDS_INSTANCE="${RDS_INSTANCE:-dupli1-production}"
ASG_NAME="${ECS_ASG_NAME:-dupli1-production-ecs-asg}"
# 2×t3.large packs all services once instance-role awsvpcTrunking is active
# (manage-web is bridge; verify with infra/scripts/shrink-ecs-asg.sh).
ASG_DESIRED="${ASG_DESIRED:-2}"
ASG_MIN="${ASG_MIN:-1}"
ASG_MAX="${ASG_MAX:-4}"
DESIRED_COUNT="${DESIRED_COUNT:-1}"
APPLY_NAT="${APPLY_NAT:-0}"
VPN_INSTANCE_ID="${VPN_INSTANCE_ID:-}"
WAIT_RDS="${WAIT_RDS:-1}"

SERVICES=(
  dupli1-redis
  dupli1-nats
  dupli1-auth
  dupli1-product
  dupli1-order
  dupli1-cart
  dupli1-payment
  dupli1-notification
  dupli1-proxy
  dupli1-web
  dupli1-manage-web
)

log() { echo "$*"; }

if [[ "$APPLY_NAT" == "1" ]]; then
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  log "Recreating NAT Gateway via Terraform (APPLY_NAT=1)..."
  (
    cd "$ROOT/infra/terraform"
    terraform init -input=false >/dev/null
    terraform apply -input=false -auto-approve \
      -target=aws_eip.nat \
      -target=aws_nat_gateway.prod \
      -target=aws_route.private_default_nat
  )
  log "  NAT apply finished"
fi

log "Starting RDS instance $RDS_INSTANCE..."
for _ in $(seq 1 40); do
  RDS_STATUS="$(aws rds describe-db-instances \
    --region "$AWS_REGION" \
    --db-instance-identifier "$RDS_INSTANCE" \
    --query 'DBInstances[0].DBInstanceStatus' \
    --output text 2>/dev/null || echo missing)"
  case "$RDS_STATUS" in
    stopped)
      aws rds start-db-instance \
        --region "$AWS_REGION" \
        --db-instance-identifier "$RDS_INSTANCE" \
        --no-cli-pager >/dev/null
      log "  RDS start requested"
      break
      ;;
    available|starting|configuring-enhanced-monitoring|backing-up|modifying)
      log "  RDS status=$RDS_STATUS"
      break
      ;;
    stopping)
      log "  RDS still stopping — waiting..."
      sleep 15
      ;;
    *)
      log "  RDS status=$RDS_STATUS — waiting..."
      sleep 15
      ;;
  esac
done

if [[ -n "$VPN_INSTANCE_ID" ]]; then
  log "Starting VPN EC2 $VPN_INSTANCE_ID..."
  aws ec2 start-instances \
    --region "$AWS_REGION" \
    --instance-ids "$VPN_INSTANCE_ID" \
    --no-cli-pager >/dev/null 2>&1 \
    && log "  VPN start requested" \
    || log "  VPN instance missing (skip)"
fi

log "Scaling ECS ASG $ASG_NAME to min=$ASG_MIN desired=$ASG_DESIRED..."
aws autoscaling update-auto-scaling-group \
  --region "$AWS_REGION" \
  --auto-scaling-group-name "$ASG_NAME" \
  --min-size "$ASG_MIN" \
  --max-size "$ASG_MAX" \
  --desired-capacity "$ASG_DESIRED" \
  --no-cli-pager
log "  ASG update requested"

if [[ "$WAIT_RDS" == "1" ]]; then
  log "Waiting for RDS to become available..."
  aws rds wait db-instance-available \
    --region "$AWS_REGION" \
    --db-instance-identifier "$RDS_INSTANCE"
  log "  RDS available"
fi

log "Scaling ECS services in cluster $CLUSTER to $DESIRED_COUNT..."
for svc in "${SERVICES[@]}"; do
  if ! aws ecs describe-services \
    --region "$AWS_REGION" \
    --cluster "$CLUSTER" \
    --services "$svc" \
    --query 'services[?status==`ACTIVE`].serviceName' \
    --output text 2>/dev/null | grep -qx "$svc"; then
    log "  $svc not active (skip)"
    continue
  fi
  aws ecs update-service \
    --region "$AWS_REGION" \
    --cluster "$CLUSTER" \
    --service "$svc" \
    --desired-count "$DESIRED_COUNT" \
    --force-new-deployment \
    --no-cli-pager >/dev/null
  log "  $svc -> desiredCount=$DESIRED_COUNT (forced new deployment)"
done

echo ""
echo "Resume initiated."
echo "  Gateway: http://dupli1-production-alb-1509499664.us-east-1.elb.amazonaws.com/gateway/health"
echo "  Wait for ECS tasks + target group health before relying on the API."
