#!/usr/bin/env bash
# render-common.sh — shared manifest rendering for the LongMemEval rig.
# Sourced by run-500.sh and render-test.sh (not executed directly).
#
# Manifest templates use ${LME_*} placeholders. envsubst runs with an explicit
# allowlist so shell syntax inside embedded scripts (the runner ConfigMap's
# slice loop) is never touched, and rendering fails loudly if a required
# variable is unset or an unknown ${LME_*} placeholder survives.

# Every LME_* placeholder that may appear in a manifest template.
LME_RENDER_VARS='${LME_NAMESPACE} ${LME_IMAGE} ${LME_IMAGE_TAG} ${LME_GRPC_PORT} ${LME_MODEL} ${LME_DATASET} ${LME_DATASET_FILE} ${LME_MODE} ${LME_CONCURRENCY} ${LME_CHUNK} ${LME_OCCURRED_AT} ${LME_RUN_ID} ${LME_RUN_MANIFEST} ${LME_ALLOW_MANIFEST_DRIFT} ${LME_MAX_CHUNK_ATTEMPTS} ${LME_CONFIG_HASH}'

# lme_render <template> — render a manifest template to stdout.
# Fails if any allowlisted variable is unset/empty, or if any ${LME_*}
# placeholder survives rendering (a variable name missing from the allowlist).
lme_render() {
    local tpl="$1" rendered var
    for var in ${LME_RENDER_VARS}; do
        var="${var#\$\{}"
        var="${var%\}}"
        # Require only what THIS template references. The allowlist is global,
        # so demanding every entry made it impossible to render one template
        # in order to compute a value another template needs — which is
        # exactly what run-500.sh does for LME_CONFIG_HASH, and it died on the
        # documented invocation. A variable that never appears here cannot
        # affect this render.
        grep -qF "\${${var}}" "${tpl}" || continue
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

# lme_sha256_12_stdin — first 12 hex chars of sha256 over stdin (portable).
lme_sha256_12_stdin() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum | cut -c1-12
    else
        shasum -a 256 | cut -c1-12
    fi
}

# lme_sha256_12 <string> — first 12 hex chars of sha256 (portable macOS/Linux).
lme_sha256_12() {
    printf '%s' "$1" | lme_sha256_12_stdin
}

# lme_build_id <repo-root> — the image tag identifying what will actually be
# built. `az acr build` uploads the working tree, not the commit, so a commit
# hash alone does not identify a build: a dirty tree would be tagged with a
# name a clean build of that commit may already own, and ACR tags are mutable.
# A clean tree yields the short commit; a dirty one yields
# <commit>-dirty-<fingerprint>, where the fingerprint covers the uncommitted
# tracked changes and the contents of every untracked, non-ignored file — the
# parts of the upload that differ from the commit.
lme_build_id() {
    local root="$1" commit fingerprint f
    commit="$(git -C "${root}" rev-parse --short HEAD)"
    if [[ -z "$(git -C "${root}" status --porcelain)" ]]; then
        printf '%s' "${commit}"
        return 0
    fi
    fingerprint="$( {
        git -C "${root}" diff HEAD --binary
        git -C "${root}" ls-files --others --exclude-standard -z |
            while IFS= read -r -d '' f; do
                printf '%s\n' "${f}"
                cat "${root}/${f}" 2>/dev/null
            done
    } | lme_sha256_12_stdin )"
    printf '%s-dirty-%s' "${commit}" "${fingerprint}"
}
