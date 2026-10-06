#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
oc_bin=${OC_BIN:-oc}
namespace=${IMAGE_NAMESPACE:-kueue-demo}
internal_registry=${INTERNAL_IMAGE_REGISTRY:-image-registry.openshift-image-registry.svc:5000}
controller_tag=${CONTROLLER_TAG:?CONTROLLER_TAG is required}
model_tag=${DECISION_MODEL_TAG:?DECISION_MODEL_TAG is required}
lock_file="$repo_root/config/images.lock.yaml"

run_oc() { "$oc_bin" "$@"; }

controller_ref=$(run_oc get imagestreamtag "intake-controller:$controller_tag" -n "$namespace" -o jsonpath='{.image.dockerImageReference}')
model_ref=$(run_oc get imagestreamtag "diffusiongemma-structured-decision:$model_tag" -n "$namespace" -o jsonpath='{.image.dockerImageReference}')

validate_ref() {
  local ref=$1 repository=$2 digest
  [[ "$ref" == "$internal_registry/$namespace/$repository@sha256:"* ]] || {
    printf 'unexpected internal image reference: %s\n' "$ref" >&2
    return 1
  }
  digest=${ref##*@sha256:}
  [[ "$digest" =~ ^[a-f0-9]{64}$ ]] || {
    printf 'image reference does not contain a sha256 digest: %s\n' "$ref" >&2
    return 1
  }
}

validate_ref "$controller_ref" intake-controller
validate_ref "$model_ref" diffusiongemma-structured-decision

old_controller_ref=$(sed -n '/intakeController:/,/diffusionGemma:/s/^[[:space:]]*pullSpec: //p' "$lock_file")
old_model_ref=$(sed -n '/diffusionGemma:/,$s/^[[:space:]]*pullSpec: //p' "$lock_file")
old_controller_tag=$(sed -n '/intakeController:/,/diffusionGemma:/s/^[[:space:]]*sourceRevision: //p' "$lock_file")
old_model_tag=$(sed -n '/diffusionGemma:/,$s/^[[:space:]]*sourceRevision: //p' "$lock_file")

replace_exact() {
  local old=$1 new=$2 file=$3
  [[ -n "$old" && -n "$new" && -f "$file" ]] || {
    printf 'cannot update image lock in %s\n' "$file" >&2
    return 1
  }
  OLD_VALUE="$old" NEW_VALUE="$new" perl -0pi -e 's/\Q$ENV{OLD_VALUE}\E/$ENV{NEW_VALUE}/g or die "expected image reference not found in $ARGV\n"' "$file"
}

replace_source_revision() {
  local old=$1 new=$2
  OLD_VALUE="$old" NEW_VALUE="$new" perl -0pi -e 's/^([ \t]*sourceRevision:[ \t]*)\Q$ENV{OLD_VALUE}\E[ \t]*$/${1}$ENV{NEW_VALUE}/m or die "expected image source revision not found in $ARGV\n"' "$lock_file"
}

replace_exact "$old_controller_ref" "$controller_ref" "$lock_file"
replace_exact "$old_controller_ref" "$controller_ref" "$repo_root/manifests/bootstrap/30-intake-controller-deployment.yaml"
replace_exact "$old_model_ref" "$model_ref" "$lock_file"
replace_exact "$old_model_ref" "$model_ref" "$repo_root/manifests/decision-model/20-deployment.yaml"
replace_source_revision "$old_controller_tag" "$controller_tag"
replace_source_revision "$old_model_tag" "$model_tag"

if [[ "$old_model_tag" != "$model_tag" ]]; then
  OLD_VALUE="$old_model_tag" NEW_VALUE="$model_tag" perl -0pi -e 's/\Q$ENV{OLD_VALUE}\E/$ENV{NEW_VALUE}/g' "$repo_root/manifests/decision-model/20-deployment.yaml"
fi

printf 'locked intake-controller: %s\n' "$controller_ref"
printf 'locked decision-model: %s\n' "$model_ref"
