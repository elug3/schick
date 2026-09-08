#!/usr/bin/env bash
# Verify awsvpc trunking on ECS hosts, then shrink ASG toward 2×t3.large.
#
# Usage:
#   bash infra/scripts/shrink-ecs-asg.sh            # dry-run
#   APPLY=1 bash infra/scripts/shrink-ecs-asg.sh    # shrink when safe
#
# Run after terraform apply of trunking user-data + instance-role policy, and
# after ASG instance refresh has replaced hosts.
set -euo pipefail

AWS_REGION="${AWS_REGION:-us-east-1}"
CLUSTER="${ECS_CLUSTER:-production}"
ASG_NAME="${ECS_ASG_NAME:-dupli1-production-ecs-asg}"
ASG_MIN="${ASG_MIN:-1}"
ASG_DESIRED="${ASG_DESIRED:-2}"
ASG_MAX="${ASG_MAX:-4}"
APPLY="${APPLY:-0}"
MIN_DENSE_HOSTS="${MIN_DENSE_HOSTS:-2}"

log() { printf '%s\n' "$*"; }

log "== ECS ASG shrink check (region=$AWS_REGION cluster=$CLUSTER) =="

INSTANCE_ARNS=$(aws ecs list-container-instances \
  --region "$AWS_REGION" \
  --cluster "$CLUSTER" \
  --query 'containerInstanceArns[]' \
  --output text)

if [[ -z "${INSTANCE_ARNS// }" || "$INSTANCE_ARNS" == "None" ]]; then
  log "ERROR: no container instances on cluster $CLUSTER"
  exit 1
fi

# shellcheck disable=SC2086
DESC=$(aws ecs describe-container-instances \
  --region "$AWS_REGION" \
  --cluster "$CLUSTER" \
  --container-instances $INSTANCE_ARNS \
  --output json)

EC2_IDS=$(python3 -c 'import json,sys; d=json.load(sys.stdin); print(" ".join(ci["ec2InstanceId"] for ci in d["containerInstances"] if ci.get("ec2InstanceId")))' <<<"$DESC")
HOST_COUNT=$(python3 -c 'import json,sys; print(len(json.load(sys.stdin)["containerInstances"]))' <<<"$DESC")
log "Registered container instances: $HOST_COUNT ($EC2_IDS)"

if [[ -z "${EC2_IDS// }" ]]; then
  log "ERROR: no EC2 instance IDs"
  exit 1
fi

# Without trunking a t3.large has 3 ENIs (1 primary + 2 task). Trunking adds a
# trunk ENI and many branch ENIs ⇒ typically ≥4 interfaces per busy host.
FILTER_VALUES=$(echo "$EC2_IDS" | tr ' ' ',')
ENI_JSON=$(aws ec2 describe-network-interfaces \
  --region "$AWS_REGION" \
  --filters "Name=attachment.instance-id,Values=$FILTER_VALUES" \
  --output json)

DENSE_HOSTS=$(python3 - "$ENI_JSON" <<'PY'
import json, sys
from collections import Counter
enis = json.loads(sys.argv[1])["NetworkInterfaces"]
counts = Counter()
for e in enis:
    att = e.get("Attachment") or {}
    iid = att.get("InstanceId")
    if iid:
        counts[iid] += 1
for iid, n in sorted(counts.items()):
    mark = "dense" if n >= 4 else "classic"
    print(f"  {iid}: {n} ENIs ({mark})", file=sys.stderr)
print(sum(1 for n in counts.values() if n >= 4))
PY
)

log "Hosts with ≥4 ENIs (trunking likely): $DENSE_HOSTS / $HOST_COUNT"

ASG=$(aws autoscaling describe-auto-scaling-groups \
  --region "$AWS_REGION" \
  --auto-scaling-group-names "$ASG_NAME" \
  --query 'AutoScalingGroups[0].{min:MinSize,desired:DesiredCapacity,max:MaxSize,n:length(Instances)}' \
  --output json)
log "Current ASG: $ASG"

SAFE=0
if [[ "$DENSE_HOSTS" -ge "$MIN_DENSE_HOSTS" ]]; then
  SAFE=1
  log "SAFE_TO_SHRINK=1"
else
  log "SAFE_TO_SHRINK=0 — apply trunking Terraform, start instance refresh, wait, re-run."
  log "  aws autoscaling start-instance-refresh --auto-scaling-group-name $ASG_NAME --preferences MinHealthyPercentage=50"
fi

if [[ "$APPLY" != "1" ]]; then
  log
  log "Dry-run only. When SAFE_TO_SHRINK=1: APPLY=1 bash $0"
  exit 0
fi

if [[ "$SAFE" != "1" ]]; then
  log "ERROR: refusing to shrink without trunking evidence."
  exit 2
fi

log "== Shrinking $ASG_NAME → min=$ASG_MIN desired=$ASG_DESIRED max=$ASG_MAX =="
aws autoscaling update-auto-scaling-group \
  --region "$AWS_REGION" \
  --auto-scaling-group-name "$ASG_NAME" \
  --min-size "$ASG_MIN" \
  --desired-capacity "$ASG_DESIRED" \
  --max-size "$ASG_MAX" \
  --no-cli-pager

log "Watch placement failures for RESOURCE:ENI; smoke-test https://dupli1.com/gateway/health"
log "Done."
