#!/usr/bin/env bash
# Opt-in cleanup of idle AWS spend found in the 2026-07-14 cost review.
# See docs/aws-cost-optimization.md.
#
# Default: dry-run (print actions only).
#   bash infra/scripts/cleanup-aws-orphans.sh
#
# Apply selected cleanups:
#   DELETE_GA=1 bash infra/scripts/cleanup-aws-orphans.sh
#   STOP_SYDNEY=1 bash infra/scripts/cleanup-aws-orphans.sh
#   DELETE_RDS_EC2=1 bash infra/scripts/cleanup-aws-orphans.sh
#   SHRINK_ASG=1 bash infra/scripts/cleanup-aws-orphans.sh
#   APPLY=1 DELETE_GA=1 SHRINK_ASG=1 bash infra/scripts/cleanup-aws-orphans.sh
set -euo pipefail

AWS_REGION="${AWS_REGION:-us-east-1}"
APPLY="${APPLY:-0}"
DELETE_GA="${DELETE_GA:-0}"
STOP_SYDNEY="${STOP_SYDNEY:-0}"
DELETE_RDS_EC2="${DELETE_RDS_EC2:-0}"
SHRINK_ASG="${SHRINK_ASG:-0}"
ASG_NAME="${ECS_ASG_NAME:-dupli1-production-ecs-asg}"
ASG_DESIRED="${ASG_DESIRED:-2}"
ASG_MIN="${ASG_MIN:-1}"
ASG_MAX="${ASG_MAX:-4}"
RDS_EC2_ID="${RDS_EC2_ID:-dupli1-ec2}"

log() { echo "$*"; }
run() {
  if [[ "$APPLY" == "1" ]]; then
    log "+ $*"
    "$@"
  else
    log "DRY-RUN: $*"
  fi
}

log "Mode: $([[ "$APPLY" == "1" ]] && echo APPLY || echo DRY-RUN)"
log

# --- Global Accelerator (empty endpoints; ~$36/mo) ---
if [[ "$DELETE_GA" == "1" ]]; then
  log "== Global Accelerators =="
  while IFS= read -r arn; do
    [[ -z "$arn" ]] && continue
    name=$(aws globalaccelerator describe-accelerator --region us-west-2 \
      --accelerator-arn "$arn" --query 'Accelerator.Name' --output text)
    log "Found accelerator: $name ($arn)"
    while IFS= read -r larn; do
      [[ -z "$larn" ]] && continue
      while IFS= read -r egarn; do
        [[ -z "$egarn" ]] && continue
        run aws globalaccelerator delete-endpoint-group --region us-west-2 \
          --endpoint-group-arn "$egarn" --no-cli-pager
      done < <(aws globalaccelerator list-endpoint-groups --region us-west-2 \
        --listener-arn "$larn" --query 'EndpointGroups[].EndpointGroupArn' --output text | tr '\t' '\n')
      run aws globalaccelerator delete-listener --region us-west-2 \
        --listener-arn "$larn" --no-cli-pager
    done < <(aws globalaccelerator list-listeners --region us-west-2 \
      --accelerator-arn "$arn" --query 'Listeners[].ListenerArn' --output text | tr '\t' '\n')
    run aws globalaccelerator update-accelerator --region us-west-2 \
      --accelerator-arn "$arn" --no-enabled --no-cli-pager
    run aws globalaccelerator delete-accelerator --region us-west-2 \
      --accelerator-arn "$arn" --no-cli-pager
  done < <(aws globalaccelerator list-accelerators --region us-west-2 \
    --query 'Accelerators[].AcceleratorArn' --output text | tr '\t' '\n')
  log
else
  log "Skip Global Accelerators (set DELETE_GA=1). Est. save ~\$36/mo."
fi

# --- Sydney test / VPN micros ---
if [[ "$STOP_SYDNEY" == "1" ]]; then
  log "== ap-southeast-2 instances =="
  for id in $(aws ec2 describe-instances --region ap-southeast-2 \
    --filters Name=instance-state-name,Values=running \
    Name=tag:Name,Values=schick-test,mweb-vpn \
    --query 'Reservations[].Instances[].InstanceId' --output text); do
    [[ -z "$id" || "$id" == "None" ]] && continue
    run aws ec2 stop-instances --region ap-southeast-2 --instance-ids "$id" --no-cli-pager
  done
  log
else
  log "Skip Sydney instances (set STOP_SYDNEY=1 after confirming unused). Est. save ~\$15–25/mo."
fi

# --- Orphan stopped RDS leftover from EC2 experiments ---
if [[ "$DELETE_RDS_EC2" == "1" ]]; then
  log "== RDS $RDS_EC2_ID =="
  status=$(aws rds describe-db-instances --region "$AWS_REGION" \
    --db-instance-identifier "$RDS_EC2_ID" \
    --query 'DBInstances[0].DBInstanceStatus' --output text 2>/dev/null || echo missing)
  if [[ "$status" == "missing" || "$status" == "None" ]]; then
    log "RDS $RDS_EC2_ID not found (ok)."
  else
    log "RDS $RDS_EC2_ID status=$status — final snapshot then delete"
    run aws rds delete-db-instance --region "$AWS_REGION" \
      --db-instance-identifier "$RDS_EC2_ID" \
      --final-db-snapshot-identifier "${RDS_EC2_ID}-final-$(date +%Y%m%d)" \
      --no-cli-pager
  fi
  log
else
  log "Skip RDS $RDS_EC2_ID (set DELETE_RDS_EC2=1). Avoids storage + 7-day auto-restart."
fi

# WARNING: only shrink after trunk ENIs exist on hosts (instance-role
# awsvpcTrunking opt-in in user-data + instance refresh). Prefer:
#   bash infra/scripts/shrink-ecs-asg.sh            # dry-run
#   APPLY=1 bash infra/scripts/shrink-ecs-asg.sh    # when SAFE_TO_SHRINK=1
# Without trunking, t3.large ≈ 2 awsvpc tasks each → need ~5 hosts.
if [[ "$SHRINK_ASG" == "1" ]]; then
  log "== ASG $ASG_NAME → min=$ASG_MIN desired=$ASG_DESIRED max=$ASG_MAX =="
  log "NOTE: Prefer infra/scripts/shrink-ecs-asg.sh (verifies trunk ENIs first)."
  log "NOTE: Temporarily disable capacity-provider managed scaling before shrink,"
  log "      or it may scale desired back up while instances drain."
  run aws autoscaling update-auto-scaling-group \
    --region "$AWS_REGION" \
    --auto-scaling-group-name "$ASG_NAME" \
    --min-size "$ASG_MIN" \
    --desired-capacity "$ASG_DESIRED" \
    --max-size "$ASG_MAX" \
    --no-cli-pager
  log
else
  log "Skip ASG shrink (set SHRINK_ASG=1). Prefer: bash infra/scripts/shrink-ecs-asg.sh"
fi

log "Done. Re-run with APPLY=1 and the flags you want to execute."
