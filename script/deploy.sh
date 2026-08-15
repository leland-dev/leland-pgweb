#!/bin/bash
#
# Build, push, and deploy this fork of pgweb to the existing ECS infrastructure
# provisioned by leland-monorepo (terraform/services/leland-shared/modules/pgweb).
#
# The Terraform module bootstraps the task definition and service, then stops
# managing them (`ignore_changes` on container_definitions / task_definition), so
# new revisions are expected to be registered out of band -- which is what this
# script does:
#
#   1. Build the image for linux/amd64, tag it with the git SHA, push it to the
#      environment's ECR repository (tags there are immutable)
#   2. Register a new task definition revision with the pgweb container repointed
#      at that image, leaving the cloudflared sidecar and everything else intact
#   3. Point the service at the new revision and wait for it to stabilize
#
# Usage: script/deploy.sh --profile staging-hosting [--environment staging]
#

set -euo pipefail

# ─── Configuration ────────────────────────────────────────────────────────────
# One ECR repository per environment, each in its own hosting account, both named
# leland-pgweb (see modules/ecr, instantiated in leland-shared/environments/*).
# The registry host is derived from the account below and verified against the
# caller identity before anything is pushed.

STAGING_ACCOUNT_ID="025066246517"
PROD_ACCOUNT_ID="026090531533"

ECR_REPOSITORY="${ECR_REPOSITORY:-leland-pgweb}"

# The pgweb task definition declares runtime_platform.cpu_architecture = X86_64,
# so the image must be amd64 even when this script runs on an Apple Silicon Mac.
BUILD_PLATFORM="linux/amd64"

# Container name inside the task definition. The task also runs a `cloudflared`
# sidecar, which this script must not touch.
CONTAINER_NAME="pgweb"

AWS_REGION="${AWS_REGION:-us-west-2}"
ENVIRONMENT="staging"
AWS_PROFILE_ARG=""
WAIT_FOR_STABLE="true"
ASSUME_YES="false"

# ─── Helpers ──────────────────────────────────────────────────────────────────

log()  { echo "==> $*"; }
warn() { echo "WARNING: $*" >&2; }
die()  { echo "ERROR: $*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Build, push, and deploy leland-pgweb to ECS.

Usage:
  script/deploy.sh --profile <aws-profile> [options]

Options:
  --profile <name>       AWS profile to use for every AWS call. Omit to use the
                         ambient credentials (AWS_PROFILE, instance role, ...).
  --environment <env>    Target environment: staging or prod. Default: staging.
  --region <region>      AWS region. Default: us-west-2.
  --no-wait              Kick off the deployment without waiting for the service
                         to reach a steady state.
  --yes                  Skip the confirmation prompt (required for prod).
  --help                 Show this message.

Environment overrides:
  ECR_REGISTRY           ECR registry host. Defaults to the hosting account for
                         the selected environment.
  ECR_REPOSITORY         ECR repository name. Default: leland-pgweb.

Examples:
  script/deploy.sh --profile staging-hosting
  script/deploy.sh --profile prod-hosting --environment prod --yes
EOF
}

# ─── Argument parsing ─────────────────────────────────────────────────────────

while [ $# -gt 0 ]; do
  case "$1" in
    --profile)
      [ $# -ge 2 ] || die "--profile requires a value"
      AWS_PROFILE_ARG="$2"
      shift 2
      ;;
    --profile=*)
      AWS_PROFILE_ARG="${1#*=}"
      shift
      ;;
    --environment|--env)
      [ $# -ge 2 ] || die "--environment requires a value"
      ENVIRONMENT="$2"
      shift 2
      ;;
    --environment=*|--env=*)
      ENVIRONMENT="${1#*=}"
      shift
      ;;
    --region)
      [ $# -ge 2 ] || die "--region requires a value"
      AWS_REGION="$2"
      shift 2
      ;;
    --region=*)
      AWS_REGION="${1#*=}"
      shift
      ;;
    --no-wait)
      WAIT_FOR_STABLE="false"
      shift
      ;;
    --yes|-y)
      ASSUME_YES="true"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done

case "$ENVIRONMENT" in
  staging) AWS_ACCOUNT_ID="$STAGING_ACCOUNT_ID" ;;
  prod)    AWS_ACCOUNT_ID="$PROD_ACCOUNT_ID" ;;
  *) die "--environment must be 'staging' or 'prod' (got: $ENVIRONMENT)" ;;
esac

ECR_REGISTRY="${ECR_REGISTRY:-${AWS_ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com}"

# Derived from the naming convention in modules/pgweb: the service and task
# definition family are both "<name>-<environment>".
CLUSTER="leland-${ENVIRONMENT}-frontend-cluster"
SERVICE="leland-pgweb-${ENVIRONMENT}"
TASK_FAMILY="leland-pgweb-${ENVIRONMENT}"

# Every AWS call goes through this so --profile is applied consistently.
aws_cmd() {
  if [ -n "$AWS_PROFILE_ARG" ]; then
    aws --profile "$AWS_PROFILE_ARG" --region "$AWS_REGION" "$@"
  else
    aws --region "$AWS_REGION" "$@"
  fi
}

# ─── Preflight ────────────────────────────────────────────────────────────────

for binary in aws docker jq git; do
  command -v "$binary" >/dev/null 2>&1 || die "$binary is required but not installed"
done

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Tag on the git SHA. --short=8 matches the Makefile's GIT_COMMIT, so the tag
# lines up with the commit the built binary reports in its version info.
#
# Deploying a dirty tree is never allowed: the tag would name a commit whose
# content it does not contain, and since ECR tags are immutable that lie would be
# permanent -- the tag could never be corrected by a later push.
if [ -n "$(git status --porcelain)" ]; then
  die "working tree has uncommitted changes. Commit them before deploying so the
     image tag matches the code it contains."
fi

IMAGE_TAG="$(git rev-parse --short=8 HEAD)"
IMAGE_URI="${ECR_REGISTRY}/${ECR_REPOSITORY}:${IMAGE_TAG}"

cat <<EOF

  Environment  : $ENVIRONMENT
  Region       : $AWS_REGION
  Account      : $AWS_ACCOUNT_ID
  Profile      : ${AWS_PROFILE_ARG:-<ambient credentials>}
  Cluster      : $CLUSTER
  Service      : $SERVICE
  Task family  : $TASK_FAMILY
  Image        : $IMAGE_URI
  Platform     : $BUILD_PLATFORM

EOF

# --profile and --environment are independent flags, so verify they agree before
# anything else: staging and prod are separate accounts, each with their own
# leland-pgweb repository, and the names alone give no hint which one you hit.
# Checked ahead of the prompt so a mismatch is caught before you confirm.
log "Verifying AWS credentials"
caller_account="$(aws_cmd sts get-caller-identity --query 'Account' --output text)" \
  || die "unable to authenticate to AWS (profile: ${AWS_PROFILE_ARG:-<ambient>})"

if [ "$caller_account" != "$AWS_ACCOUNT_ID" ]; then
  die "profile ${AWS_PROFILE_ARG:-<ambient>} resolves to account $caller_account,
     but --environment $ENVIRONMENT targets account $AWS_ACCOUNT_ID."
fi

if [ "$ASSUME_YES" != "true" ]; then
  printf "Deploy? [y/N] "
  read -r reply
  case "$reply" in
    [yY]|[yY][eE][sS]) ;;
    *) die "aborted" ;;
  esac
fi

# ─── 1. Build and push ────────────────────────────────────────────────────────

log "Logging in to ECR"
aws_cmd ecr get-login-password | docker login --username AWS --password-stdin "$ECR_REGISTRY"

# Tags are immutable: pushing an existing one fails outright. When the tag is
# already there the image is by definition this commit's build, so redeploying
# the same commit reuses it instead of wasting a build on a doomed push.
if aws_cmd ecr describe-images \
     --repository-name "$ECR_REPOSITORY" \
     --image-ids "imageTag=$IMAGE_TAG" >/dev/null 2>&1; then
  log "$IMAGE_TAG is already in ECR; reusing it (tags are immutable)"
else
  log "Building image for $BUILD_PLATFORM"
  docker build --platform "$BUILD_PLATFORM" -t "$IMAGE_URI" .

  log "Pushing $IMAGE_URI"
  docker push "$IMAGE_URI"
fi

# ─── 2. Register a new task definition revision ───────────────────────────────

log "Fetching current task definition: $TASK_FAMILY"
current_taskdef="$(aws_cmd ecs describe-task-definition \
  --task-definition "$TASK_FAMILY" \
  --include TAGS)"

# Guard against a silent no-op: if the container name ever changes in Terraform,
# the image swap below would match nothing and we would redeploy the old image.
jq -e --arg name "$CONTAINER_NAME" \
  '[.taskDefinition.containerDefinitions[].name] | index($name)' \
  <<<"$current_taskdef" >/dev/null \
  || die "no container named '$CONTAINER_NAME' in task definition $TASK_FAMILY"

# Repoint only the pgweb container, drop the read-only fields that
# describe-task-definition returns but register-task-definition rejects, and
# carry the tags across so revisions do not lose them.
new_taskdef="$(jq \
  --arg name "$CONTAINER_NAME" \
  --arg image "$IMAGE_URI" \
  '
  (.tags // []) as $tags
  | .taskDefinition
  | .containerDefinitions = (
      .containerDefinitions
      | map(if .name == $name then .image = $image else . end)
    )
  | {
      family,
      taskRoleArn,
      executionRoleArn,
      networkMode,
      containerDefinitions,
      volumes,
      placementConstraints,
      requiresCompatibilities,
      cpu,
      memory,
      pidMode,
      ipcMode,
      proxyConfiguration,
      inferenceAccelerators,
      ephemeralStorage,
      runtimePlatform
    }
  | with_entries(select(.value != null))
  | if ($tags | length) > 0 then . + { tags: $tags } else . end
  ' <<<"$current_taskdef")"

log "Registering new task definition revision"
new_taskdef_arn="$(aws_cmd ecs register-task-definition \
  --cli-input-json "$new_taskdef" \
  --query 'taskDefinition.taskDefinitionArn' \
  --output text)"

log "Registered $new_taskdef_arn"

# ─── 3. Deploy ────────────────────────────────────────────────────────────────

log "Updating service $SERVICE"
aws_cmd ecs update-service \
  --cluster "$CLUSTER" \
  --service "$SERVICE" \
  --task-definition "$new_taskdef_arn" \
  --query 'service.deployments[0].{status:status,taskDefinition:taskDefinition}' \
  --output table

if [ "$WAIT_FOR_STABLE" != "true" ]; then
  log "Deployment started (not waiting for it to stabilize)"
  exit 0
fi

log "Waiting for the service to reach a steady state (this takes a few minutes)"
if aws_cmd ecs wait services-stable --cluster "$CLUSTER" --services "$SERVICE"; then
  log "Deployed $IMAGE_URI to $SERVICE"
else
  die "service did not stabilize. Check the deployment and the task logs:
     aws ecs describe-services --cluster $CLUSTER --services $SERVICE
     CloudWatch log group: /ecs/leland-pgweb-${ENVIRONMENT}"
fi
