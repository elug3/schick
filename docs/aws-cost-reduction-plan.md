# AWS cost reduction plan

Action plan to cut Dupli1 AWS spend from the **~$350–420/mo** run-rate (6×`t3.large` + idle orphans) down to **~$210–230/mo** core, or lower when paused.

Companion review with inventory and Cost Explorer detail: [aws-cost-optimization.md](aws-cost-optimization.md).  
**July month-end report (2026-07-26):** [aws-july-2026-cost-report.md](aws-july-2026-cost-report.md) — MTD ~$365, forecast ~$474; Phase 1–2 still open.

## Target outcomes

| Mode | Monthly estimate | When to use |
|------|------------------|-------------|
| **A — Steady production (shrunk)** | **~$210–230** | Site must stay up 24/7 |
| **B — Demo / idle (paused)** | **~$50–70** | ALB + NAT still up; compute/RDS stopped |
| **C — Deep idle** | **~$20–40** | Mode B + `DELETE_NAT=1`; slower resume |
| **D — Single EC2 Compose** | **~$30–60** | Accept [deployment-ec2.md](deployment-ec2.md) trade-offs |

Do **not** delete ALB, Route53, or `dupli1-production` RDS unless moving to Mode D.

---

## Phase 0 — Baseline (recorded 2026-07-14)

Captured live from account `845061289093` before Phase 1–2 changes. Use these numbers to verify savings after shrink.

### ASG / compute

| Metric | Value |
|--------|-------|
| ASG | `dupli1-production-ecs-asg` |
| min / desired / max | **5 / 6 / 6** |
| InService instances | **6× `t3.large`** (3× `us-east-1a`, 3× `us-east-1b`) |
| Instance IDs | `i-019641ba7c852ddff`, `i-0518eeacebf2e16e8`, `i-0558d8b359f8e2b36`, `i-0895fd2bd3002f8b1`, `i-0e451f326f109f6b1`, `i-0f86f5248e5479f5a` |
| ECS services | 11 ACTIVE, each `desired=1` / `running=1`, capacity provider `dupli1-production-ec2` |

### Daily burn (Cost Explorer, unblended, estimated)

| Date | Total $/day | Notes |
|------|-------------|-------|
| 2026-06-30 | $4.96 | Mostly idle |
| 2026-07-01 | $27.75 | Fargate still on |
| 2026-07-02 | $16.75 | Fargate tapering |
| 2026-07-03 | $11.83 | Fargate ends this day |
| 2026-07-04–06 | $8.30–$8.97 | Mixed |
| 2026-07-07 | $6.08 | |
| 2026-07-08–10 | **$2.92–$3.06** | Pause-ish floor |
| 2026-07-11 | $4.63 | Ramping |
| 2026-07-12 | **$9.48** | Full ASG-ish |
| 2026-07-13 | **$7.41** | Full ASG-ish |

**Baseline daily burn to beat (full stack, oversized ASG):** average of Jul 12–13 = **~$8.45/day** (~**$255/mo** extrapolated from those two days alone; full-month with 6×`t3.large` 24/7 is still estimated **~$350–420** once EC2 hours accumulate).

**Pause floor reference:** Jul 8–10 ≈ **~$3/day**.

### Last 7 days by service (Jul 7–13)

| Service | Cost |
|---------|------|
| EC2 Compute | $15.16 |
| Global Accelerator | $7.85 |
| VPC (NAT) | $5.59 |
| EC2 Other | $4.24 |
| RDS | $2.40 |
| ELB | $0.81 |

### Orphans still present at baseline

| Resource | State |
|----------|-------|
| Global Accelerator `MyAcc`, `MyAccelerator` | enabled, DEPLOYED |
| `dupli1-vpn` (`t3.micro`, us-east-1) | **stopped** 2026-07-14; EIP disassociated (admin via `manage.dupli1.com`) |
| `schick-test`, `mweb-vpn` (`t3.micro`, ap-southeast-2) | running |

### Success criteria after Phase 1–2

| Check | Baseline | Target |
|-------|----------|--------|
| ASG instances | 6 | **2** |
| ASG min/desired/max | 5/6/6 | **1/2/4** |
| Global Accelerator | 2 enabled | **0** |
| Typical full-stack $/day | ~$8.45 | **~$5–6** (then ~$210–230/mo steady) |

Re-query commands (same as original Phase 0):

```bash
export AWS_REGION=us-east-1

# ASG size
aws autoscaling describe-auto-scaling-groups \
  --auto-scaling-group-names dupli1-production-ecs-asg \
  --query 'AutoScalingGroups[0].{min:MinSize,desired:DesiredCapacity,max:MaxSize,n:length(Instances)}'

# Daily totals (adjust dates)
aws ce get-cost-and-usage \
  --time-period Start=2026-07-01,End=2026-07-14 \
  --granularity DAILY --metrics UnblendedCost
```

---

## Phase 1 — Delete pure waste (same day, low risk)

**Est. save: ~$50–60/mo** · No Dupli1 traffic dependency.

| Step | Action | Save |
|------|--------|------|
| 1.1 | Delete empty Global Accelerators `MyAcc`, `MyAccelerator` | ~$36/mo |
| 1.2 | Confirm Sydney `schick-test` / `mweb-vpn` unused → stop (then terminate later) | ~$15–25/mo |
| 1.3 | Snapshot + delete stopped RDS `dupli1-ec2` | storage + avoids 7-day restart |

```bash
# Dry-run first
DELETE_GA=1 STOP_SYDNEY=1 DELETE_RDS_EC2=1 bash infra/scripts/cleanup-aws-orphans.sh

# Apply
APPLY=1 DELETE_GA=1 bash infra/scripts/cleanup-aws-orphans.sh

# Only after confirming Sydney VMs are disposable:
APPLY=1 STOP_SYDNEY=1 bash infra/scripts/cleanup-aws-orphans.sh

# Only after confirming dupli1-ec2 has no needed data:
APPLY=1 DELETE_RDS_EC2=1 bash infra/scripts/cleanup-aws-orphans.sh
```

**Done when:** `list-accelerators` is empty (or disabled/gone); Sydney instances stopped if flagged; `dupli1-ec2` absent or deleting.

---

## Phase 2 — Shrink ECS ASG (largest Dupli1 saving)

**Est. save: ~$240–300/mo** · Trunking via instance-role user-data opt-in + `manage-web` on bridge; 2×`t3.large` is enough.

| Step | Action |
|------|--------|
| 2.0 | `terraform apply` (trunking IAM/user-data, account default, manage-web bridge, ASG defaults 2/1/4) |
| 2.0b | Wait for ASG instance refresh (or `start-instance-refresh`) so hosts re-register with trunk ENIs |
| 2.0c | `bash infra/scripts/shrink-ecs-asg.sh` — expect `SAFE_TO_SHRINK=1` |
| 2.1 | `APPLY=1 bash infra/scripts/shrink-ecs-asg.sh` (or confirm Terraform already at desired=2) |
| 2.2 | Wait for extra instances to terminate; confirm all services `runningCount=1` |
| 2.3 | Smoke-test `https://dupli1.com` gateway health + login + catalog + `https://manage.dupli1.com` |
| 2.4 | Confirm `terraform plan` does not scale ASG back up |

```bash
# Live shrink (or use Terraform apply with updated defaults)
APPLY=1 SHRINK_ASG=1 bash infra/scripts/cleanup-aws-orphans.sh

# Watch placement
watch -n 15 'aws ecs describe-services --cluster production \
  --services dupli1-auth dupli1-product dupli1-order dupli1-cart dupli1-payment \
  dupli1-notification dupli1-proxy dupli1-web dupli1-manage-web dupli1-redis dupli1-nats \
  --query "services[].{n:serviceName,r:runningCount,d:desiredCount,e:events[0].message}"'
```

Terraform defaults in-repo are already **2 / 1 / 4** (`infra/terraform/variables.tf`). After shrink:

```bash
cd infra/terraform && terraform plan   # expect ASG size match, not a scale-up
```

**Done when:** 2 ECS instances; all 11 services healthy; public site OK for ~30 minutes.

**Rollback:** `ASG_DESIRED=5 ASG_MIN=5 ASG_MAX=6 APPLY=1 SHRINK_ASG=1 bash infra/scripts/cleanup-aws-orphans.sh`

---

## Phase 3 — Operating rules (keep the saving)

| Rule | Command / habit |
|------|-----------------|
| Pause when not demoing | `bash infra/scripts/pause-aws.sh` |
| Multi-day pause | `DELETE_NAT=1 bash infra/scripts/pause-aws.sh` |
| Resume | `bash infra/scripts/resume-aws.sh` (use `APPLY_NAT=1` if NAT was deleted) |
| Never leave ASG min ≥ 5 | Trunking removes the old ENI reason for 5 hosts |
| Prefer OIDC for CI keys | Avoid long-lived access keys (see TODO) |

Pause/resume now include **cart** and **payment**.

**Done when:** team uses pause for idle weeks; monthly bill reflects Mode A or B, not 6× large.

---

## Phase 4 — Optional further cuts

Only if Mode A is still too high:

| Option | Extra save | Trade-off |
|--------|------------|-----------|
| Stop `dupli1-vpn` when unused | ~$8–12/mo | **Done 2026-07-14** — admin is public at `manage.dupli1.com` |
| VPC endpoints (ECR/Logs/Secrets) ± drop NAT | up to ~$32/mo NAT | Endpoint hourly fees; more Terraform |
| Right-size to `t3.medium` after Phase 2 | ~$30–40/mo | Tight for `manage-web` (1 vCPU) |
| Move to single EC2 Compose (Mode D) | large (drop ALB/NAT/RDS) | Ops model change — [deployment-ec2.md](deployment-ec2.md) |

---

## Expected burn after each phase

```text
Today (6× t3.large + GA + Sydney)     ~$350–420/mo
After Phase 1 (drop GA/orphans)       ~$300–360/mo
After Phase 2 (2× t3.large)           ~$210–230/mo   ← primary goal
After Phase 3 (paused most days)      ~$50–70/mo average if often idle
```

Verify with Cost Explorer 3–5 days after Phase 2 (`EC2 - Compute` and `EC2 - Other` should fall; `Global Accelerator` → $0).

---

## Checklist

- [x] Phase 0 baseline captured (**2026-07-14** — ASG 5/6/6 ×6 `t3.large`; ~$8.45/day Jul 12–13)
- [x] Stopped `dupli1-vpn`; public admin at `https://manage.dupli1.com`
- [x] Phase 1.1 Global Accelerators deleted (**2026-07-26**)
- [x] Phase 1.2 Sydney stopped (**2026-07-26** — `schick-test`, `mweb-vpn`)
- [x] Phase 1.3 `dupli1-ec2` deleted (**2026-07-26** — snapshot `dupli1-ec2-final-20260726`)
- [x] Released idle VPN EIP (**2026-07-26**)
- [ ] Phase 2 ASG at 2× — apply trunking user-data + manage-web bridge, instance refresh, then `infra/scripts/shrink-ecs-asg.sh`
- [ ] Phase 2 prerequisite: hosts show ≥4 ENIs each (`SAFE_TO_SHRINK=1`)
- [ ] Phase 2 Terraform plan does not scale ASG up
- [ ] Phase 3 pause/resume documented for operators
- [ ] Phase 4 decided (defer / endpoints / EC2 Compose)

**Note:** Phase 1 orphans removed (2026-07-26). Phase 2 unblock: instance role self-opts into `awsvpcTrunking` in user-data (plus account default); `manage-web` uses bridge like `web`. See [deployment-aws.md](deployment-aws.md).

## Owners / scripts

| Artifact | Role |
|----------|------|
| `infra/scripts/cleanup-aws-orphans.sh` | Phase 1–2 opt-in actions |
| `infra/scripts/pause-aws.sh` / `resume-aws.sh` | Phase 3 idle control |
| `infra/terraform/` | Persistent ASG sizing |
| [aws-cost-optimization.md](aws-cost-optimization.md) | Why / evidence |
