#!/usr/bin/env bash
set -euo pipefail

# E2E test script for the non-agentic investigation pipeline.
# Deploys scenarios against a local cluster (minikube), triggers rollouts,
# and validates investigation results via the status API (trace mode).
# Falls back to log scraping for assertions that require log-only data
# (e.g. supersede cancellation messages).
#
# Prerequisites:
#   - minikube running
#   - helm chart installed with:
#       investigation.mode=runbook
#       dispatch.mode=slack
#       dispatch.slackWebhookUrl=TEST
#       tuning.debounceSeconds=5      (speed up tests)
#       logging.trace=true             (enables status API on port 8081)

NAMESPACE="${E2E_NAMESPACE:-e2e-test}"
RELEASE="${E2E_RELEASE:-deploy-monitor}"
RELEASE_NS="${E2E_RELEASE_NS:-rollout-monitor}"
CHART_DIR="$(cd "$(dirname "$0")/../chart/deploy-monitor" && pwd)"
STATUS_API_PORT="${E2E_STATUS_API_PORT:-8081}"
LOCAL_PORT="${E2E_LOCAL_PORT:-18081}"
PORT_FWD_PID=""

# Per-test timeout: debounce(5s) + config-error-window(90s) + soak(60s) + buffer
TEST_TIMEOUT="${E2E_TEST_TIMEOUT:-240}"

PASSED=0
FAILED=0
TOTAL=0
FAILURES=()

# ---------- colours ----------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # no colour

# ---------- prereq checks ----------
check_prereqs() {
    local missing=()
    for cmd in kubectl minikube helm jq; do
        if ! command -v "$cmd" &>/dev/null; then
            missing+=("$cmd")
        fi
    done
    if (( ${#missing[@]} > 0 )); then
        echo -e "${RED}Missing required tools: ${missing[*]}${NC}"
        exit 1
    fi

    if ! minikube status --format='{{.Host}}' 2>/dev/null | grep -q Running; then
        echo -e "${RED}minikube is not running${NC}"
        exit 1
    fi
}

# ---------- kubeconfig ----------
# Generate an in-cluster kubeconfig and store it in a ConfigMap so the monitor
# can discover clusters via KUBECONFIG_DIR.
create_kubeconfig_configmap() {
    kubectl create namespace "$RELEASE_NS" 2>/dev/null || true

    local sa_base="/var/run/secrets/kubernetes.io/serviceaccount"
    cat <<KUBEEOF | kubectl apply -n "$RELEASE_NS" -f -
apiVersion: v1
kind: ConfigMap
metadata:
  name: kubeconfig
data:
  minikube.yaml: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        server: https://kubernetes.default.svc
        certificate-authority: ${sa_base}/ca.crt
      name: minikube
    contexts:
    - context:
        cluster: minikube
        user: in-cluster
      name: minikube
    current-context: minikube
    users:
    - name: in-cluster
      user:
        tokenFile: ${sa_base}/token
KUBEEOF

    echo "Kubeconfig ConfigMap created."
}

# ---------- monitor deployment ----------
deploy_monitor() {
    echo "Deploying monitor with investigation.mode=runbook ..."

    create_kubeconfig_configmap

    # Build local image and load into minikube
    eval "$(minikube docker-env)"
    docker build -t rollout-monitor:e2e "$CHART_DIR/../.." 2>/dev/null

    helm upgrade --install "$RELEASE" "$CHART_DIR" \
        --namespace "$RELEASE_NS" --create-namespace \
        --set image.repository=rollout-monitor \
        --set image.tag=e2e \
        --set image.pullPolicy=Never \
        --set kubeconfig.configMapName=kubeconfig \
        --set investigation.mode=runbook \
        --set dispatch.mode=slack \
        --set dispatch.slackWebhookUrl=TEST \
        --set tuning.debounceSeconds=5 \
        --set persistence.enabled=true \
        --set logging.trace=true \
        --set "namespaceFilter.denylist={kube-system,kube-public,kube-node-lease}" \
        --wait --timeout 120s

    # Wait for monitor pod to be ready
    kubectl rollout status deployment/"$RELEASE" \
        -n "$RELEASE_NS" --timeout=60s

    echo "Monitor deployed."
}

# ---------- helpers ----------

# Returns the monitor pod name
monitor_pod() {
    kubectl get pods -n "$RELEASE_NS" -l "app.kubernetes.io/instance=$RELEASE" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null
}

# Returns the kubectl context name (used as cluster ID in deployment keys)
cluster_id() {
    kubectl config current-context
}

# ---------- status API port-forward ----------

start_port_forward() {
    local pod
    pod="$(monitor_pod)"
    kubectl port-forward "$pod" -n "$RELEASE_NS" "${LOCAL_PORT}:${STATUS_API_PORT}" &>/dev/null &
    PORT_FWD_PID=$!

    # Wait for port-forward to be ready
    local deadline=$((SECONDS + 15))
    while (( SECONDS < deadline )); do
        if curl -s -o /dev/null -w '' "http://localhost:${LOCAL_PORT}/api/v1/investigations" 2>/dev/null; then
            echo "Status API port-forward ready (pid ${PORT_FWD_PID})"
            return 0
        fi
        sleep 1
    done

    echo -e "${RED}Failed to establish port-forward to status API${NC}"
    stop_port_forward
    return 1
}

stop_port_forward() {
    if [[ -n "$PORT_FWD_PID" ]]; then
        kill "$PORT_FWD_PID" 2>/dev/null || true
        wait "$PORT_FWD_PID" 2>/dev/null || true
        PORT_FWD_PID=""
    fi
}

# wait_for_investigation_api <deployment-name> <expected-result> <timeout-seconds>
#   Polls the status API for a result matching the deployment.
#   Returns 0 if result matches expected, 1 otherwise.
wait_for_investigation_api() {
    local deploy_name="$1"
    local expected="$2"
    local timeout="${3:-$TEST_TIMEOUT}"
    local deadline=$((SECONDS + timeout))

    while (( SECONDS < deadline )); do
        local response
        response=$(curl -s "http://localhost:${LOCAL_PORT}/api/v1/investigations/${NAMESPACE}/${deploy_name}" 2>/dev/null || true)

        # Skip if 404 or empty (investigation not yet complete)
        if [[ -n "$response" ]] && ! echo "$response" | grep -q "not found"; then
            local result
            result=$(echo "$response" | jq -r '.result' 2>/dev/null || true)

            if [[ -n "$result" && "$result" != "null" ]]; then
                if [[ "$result" == "$expected" ]]; then
                    return 0
                else
                    echo -e "  ${RED}Expected ${expected}, got ${result}${NC}"
                    return 1
                fi
            fi
        fi
        sleep 5
    done

    echo -e "  ${RED}Timed out waiting for investigation result via API (expected ${expected})${NC}"
    return 1
}

# wait_for_investigation <deployment-name> <expected-result> <timeout-seconds>
#   Polls monitor logs for "investigation report (test mode)" matching the deployment.
#   Returns 0 if result matches expected, 1 otherwise.
wait_for_investigation() {
    local deploy_name="$1"
    local expected="$2"
    local timeout="${3:-$TEST_TIMEOUT}"
    local cid
    cid="$(cluster_id)"
    local key="${cid}/${NAMESPACE}/${deploy_name}"
    local pod
    pod="$(monitor_pod)"
    local since
    since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    local deadline=$((SECONDS + timeout))
    local result=""

    while (( SECONDS < deadline )); do
        # Grep monitor logs for investigation report matching our deployment
        result=$(kubectl logs "$pod" -n "$RELEASE_NS" --since-time="$since" 2>/dev/null \
            | grep '"investigation report (test mode)"' \
            | grep "\"deployment\":\"${key}\"" \
            | tail -1 \
            | sed 's/.*"result":"\([^"]*\)".*/\1/' \
            || true)

        if [[ -n "$result" ]]; then
            if [[ "$result" == "$expected" ]]; then
                return 0
            else
                echo -e "  ${RED}Expected ${expected}, got ${result}${NC}"
                return 1
            fi
        fi
        sleep 5
    done

    echo -e "  ${RED}Timed out waiting for investigation result (expected ${expected})${NC}"
    return 1
}

# wait_for_log_pattern <pattern> <timeout-seconds>
#   Waits for a log line matching the grep pattern. Returns 0 when found.
wait_for_log_pattern() {
    local pattern="$1"
    local timeout="${2:-60}"
    local pod
    pod="$(monitor_pod)"
    local since
    since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    local deadline=$((SECONDS + timeout))

    while (( SECONDS < deadline )); do
        if kubectl logs "$pod" -n "$RELEASE_NS" --since-time="$since" 2>/dev/null \
            | grep -q "$pattern"; then
            return 0
        fi
        sleep 3
    done
    return 1
}

# setup_deployment <name> <image> [extra kubectl args...]
#   Creates a deployment and waits for it to be fully rolled out (baseline).
setup_deployment() {
    local name="$1"
    local image="$2"
    shift 2

    kubectl create namespace "$NAMESPACE" 2>/dev/null || true

    kubectl create deployment "$name" \
        --image="$image" \
        --namespace="$NAMESPACE" \
        "$@" 2>/dev/null || true

    kubectl rollout status deployment/"$name" \
        -n "$NAMESPACE" --timeout=120s 2>/dev/null || true

    # Wait extra for the debouncer to settle and baseline investigation to complete
    sleep 10
}

# cleanup <name>
#   Deletes deployment, ignores not-found.
cleanup() {
    local name="$1"
    kubectl delete deployment "$name" -n "$NAMESPACE" --ignore-not-found --timeout=30s 2>/dev/null || true
    # Small gap between tests so logs don't bleed
    sleep 3
}

# Record the log start timestamp so we only look at logs from this test
mark_log_start() {
    LOG_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    sleep 1
}

# wait_for_investigation_since <deployment-name> <expected-result> <timeout-seconds>
#   Like wait_for_investigation but uses LOG_START timestamp.
wait_for_investigation_since() {
    local deploy_name="$1"
    local expected="$2"
    local timeout="${3:-$TEST_TIMEOUT}"
    local cid
    cid="$(cluster_id)"
    local key="${cid}/${NAMESPACE}/${deploy_name}"
    local pod
    pod="$(monitor_pod)"
    local deadline=$((SECONDS + timeout))
    local result=""

    while (( SECONDS < deadline )); do
        result=$(kubectl logs "$pod" -n "$RELEASE_NS" --since-time="$LOG_START" 2>/dev/null \
            | grep '"investigation report (test mode)"' \
            | grep "\"deployment\":\"${key}\"" \
            | tail -1 \
            | sed 's/.*"result":"\([^"]*\)".*/\1/' \
            || true)

        if [[ -n "$result" ]]; then
            if [[ "$result" == "$expected" ]]; then
                return 0
            else
                echo -e "  ${RED}Expected ${expected}, got ${result}${NC}"
                return 1
            fi
        fi
        sleep 5
    done

    echo -e "  ${RED}Timed out waiting for investigation result (expected ${expected})${NC}"
    return 1
}

# ---------- test runner ----------

run_test() {
    local num="$1"
    local total="$2"
    local name="$3"
    local fn="$4"
    TOTAL=$((TOTAL + 1))

    printf "[%d/%d] %-45s " "$num" "$total" "$name"

    if $fn; then
        echo -e "${GREEN}PASS${NC}"
        PASSED=$((PASSED + 1))
    else
        echo -e "${RED}FAIL${NC}"
        FAILED=$((FAILED + 1))
        FAILURES+=("$name")
    fi
}

# ---------- test scenarios ----------

# 1. Healthy rollout: nginx:1.25 → nginx:1.26 → expect SUCCESS
test_healthy_rollout() {
    local name="e2e-success"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=nginx:1.26"

    wait_for_investigation_api "$name" "SUCCESS"
    local rc=$?
    cleanup "$name"
    return $rc
}

# 2. Bad image tag: nginx:doesnotexist → expect FAILED
test_bad_image() {
    local name="e2e-badimage"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=nginx:doesnotexist"

    wait_for_investigation_api "$name" "FAILED"
    local rc=$?
    cleanup "$name"
    return $rc
}

# 3. CrashLoopBackOff: busybox with invalid command → expect FAILED
test_crashloop() {
    local name="e2e-crashloop"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    # busybox with no command exits immediately → crash loop
    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=busybox:1.36"

    wait_for_investigation_api "$name" "FAILED"
    local rc=$?
    cleanup "$name"
    return $rc
}

# 4. Missing ConfigMap: envFrom referencing non-existent ConfigMap → expect FAILED
test_missing_configmap() {
    local name="e2e-configerr"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    # Patch to add envFrom referencing a non-existent ConfigMap
    kubectl patch deployment "$name" -n "$NAMESPACE" --type='json' \
        -p='[{"op":"add","path":"/spec/template/spec/containers/0/envFrom","value":[{"configMapRef":{"name":"does-not-exist"}}]}]'

    wait_for_investigation_api "$name" "FAILED"
    local rc=$?

    # Clean up the configmap ref before deleting
    cleanup "$name"
    return $rc
}

# 5. Deleted mid-rollout: delete deployment while investigation is running → expect DELETED
test_deleted() {
    local name="e2e-deleted"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    # Use a bad image so the investigation takes a while polling
    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=nginx:doesnotexist-delete-test"

    # Wait for investigation to start, then delete
    sleep 15
    kubectl delete deployment "$name" -n "$NAMESPACE" --ignore-not-found

    wait_for_investigation_api "$name" "DELETED"
    local rc=$?
    # Already deleted
    return $rc
}

# 6. Supersede: two rapid image changes → first cancelled, second gets a result
#    Uses logs to verify the supersede cancellation (not visible in the status API
#    since the cache is last-1 and the cancelled investigation never writes a result).
test_supersede() {
    local name="e2e-supersede"
    cleanup "$name"
    setup_deployment "$name" "nginx:1.25"

    mark_log_start
    # First image change — will start investigation
    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=nginx:1.26"
    # Wait longer than debounce window (5s) so the first event fires and starts
    # an investigation, then supersede it with a second change
    sleep 10
    kubectl set image deployment/"$name" -n "$NAMESPACE" "*=nginx:1.27"

    # Check two things:
    #   1. Supersede log message (only visible in logs)
    #   2. Final investigation result via status API
    local pod
    pod="$(monitor_pod)"
    local deadline=$((SECONDS + TEST_TIMEOUT))
    local got_supersede=false
    local got_result=false

    while (( SECONDS < deadline )); do
        # Check logs for supersede message
        if ! $got_supersede; then
            if kubectl logs "$pod" -n "$RELEASE_NS" --since-time="$LOG_START" 2>/dev/null \
                | grep -q '"superseding in-flight investigation"'; then
                got_supersede=true
            fi
        fi

        # Check status API for the final result
        if ! $got_result; then
            local response
            response=$(curl -s "http://localhost:${LOCAL_PORT}/api/v1/investigations/${NAMESPACE}/${name}" 2>/dev/null || true)
            if [[ -n "$response" ]] && ! echo "$response" | grep -q "not found"; then
                local result
                result=$(echo "$response" | jq -r '.result' 2>/dev/null || true)
                if [[ -n "$result" && "$result" != "null" ]]; then
                    got_result=true
                fi
            fi
        fi

        if $got_supersede && $got_result; then
            break
        fi
        sleep 5
    done

    cleanup "$name"

    if $got_supersede && $got_result; then
        return 0
    fi

    if ! $got_supersede; then
        echo -e "  ${RED}No supersede log found${NC}"
    fi
    if ! $got_result; then
        echo -e "  ${RED}No investigation result after supersede${NC}"
    fi
    return 1
}

# ---------- main ----------

main() {
    echo "========================================="
    echo " E2E Investigation Pipeline Tests"
    echo "========================================="
    echo ""

    check_prereqs

    deploy_monitor

    kubectl create namespace "$NAMESPACE" 2>/dev/null || true

    # Start port-forward to status API
    start_port_forward
    trap 'stop_port_forward' EXIT

    echo ""
    echo "Running tests..."
    echo ""

    run_test 1 6 "Healthy rollout"       test_healthy_rollout
    run_test 2 6 "Bad image tag"         test_bad_image
    run_test 3 6 "CrashLoopBackOff"      test_crashloop
    run_test 4 6 "Missing ConfigMap"     test_missing_configmap
    run_test 5 6 "Deleted mid-rollout"   test_deleted
    run_test 6 6 "Supersede"             test_supersede

    echo ""
    echo "========================================="
    if (( FAILED == 0 )); then
        echo -e "${GREEN}Results: ${PASSED}/${TOTAL} passed${NC}"
    else
        echo -e "${RED}Results: ${PASSED}/${TOTAL} passed, ${FAILED} failed${NC}"
        echo ""
        echo "Failed tests:"
        for f in "${FAILURES[@]}"; do
            echo "  - $f"
        done
    fi
    echo "========================================="

    # Cleanup namespace
    kubectl delete namespace "$NAMESPACE" --ignore-not-found 2>/dev/null || true

    exit "$FAILED"
}

main "$@"
