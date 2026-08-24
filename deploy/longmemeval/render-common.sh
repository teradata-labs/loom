#!/usr/bin/env bash
# render-common.sh — shared manifest rendering for the LongMemEval rig.
# Sourced by run-500.sh and render-test.sh (not executed directly).
#
# Manifest templates use ${LME_*} placeholders. envsubst runs with an explicit
# allowlist so shell syntax inside embedded scripts (the runner ConfigMap's
# slice loop) is never touched, and rendering fails loudly if a required
# variable is unset or an unknown ${LME_*} placeholder survives.

# Every LME_* placeholder that may appear in a manifest template.
LME_RENDER_VARS='${LME_NAMESPACE} ${LME_IMAGE} ${LME_IMAGE_TAG} ${LME_GRPC_PORT} ${LME_MODEL} ${LME_DATASET} ${LME_DATASET_FILE} ${LME_MODE} ${LME_CONCURRENCY} ${LME_CHUNK} ${LME_OCCURRED_AT} ${LME_RUN_ID} ${LME_RUN_MANIFEST} ${LME_ALLOW_MANIFEST_DRIFT}'

# lme_render <template> — render a manifest template to stdout.
# Fails if any allowlisted variable is unset/empty, or if any ${LME_*}
# placeholder survives rendering (a variable name missing from the allowlist).
lme_render() {
    local tpl="$1" rendered var
    for var in ${LME_RENDER_VARS}; do
        var="${var#\$\{}"
        var="${var%\}}"
        if [[ -z "${!var:-}" ]]; then
            echo "lme_render: required variable ${var} is unset or empty (rendering ${tpl})" >&2
            return 1
        fi
    done
    rendered="$(envsubst "${LME_RENDER_VARS}" < "${tpl}")" || return 1
    if grep -q '\${LME_' <<< "${rendered}"; then
        echo "lme_render: unrendered LME_* placeholder in ${tpl} (add it to LME_RENDER_VARS):" >&2
        grep -n '\${LME_' <<< "${rendered}" >&2
        return 1
    fi
    printf '%s\n' "${rendered}"
}

# lme_sha256_12 <string> — first 12 hex chars of sha256 (portable macOS/Linux).
lme_sha256_12() {
    if command -v sha256sum >/dev/null 2>&1; then
        printf '%s' "$1" | sha256sum | cut -c1-12
    else
        printf '%s' "$1" | shasum -a 256 | cut -c1-12
    fi
}
