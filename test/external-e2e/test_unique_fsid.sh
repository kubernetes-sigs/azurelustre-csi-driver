#!/bin/bash

# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# End-to-end tests for the unique_fsid mount option.
#
# Requires:
#   - kubectl configured for an AKS cluster with the CSI driver installed
#   - Lustre >= 2.15.8 kmod on the nodes
#   - An AMLFS filesystem accessible from the cluster
#
# Usage:
#   LUSTRE_FS_NAME=lustrefs LUSTRE_MGS_IP=10.1.0.7 ./test_unique_fsid.sh
#
# Tests:
#   1. unique_fsid auto-injected in mount options
#   2. Two volumes on same node get different f_fsid
#   3. no_unique_fsid suppresses auto-injection and produces same f_fsid
#   4. Unmounting one volume doesn't affect another

set -o errexit
set -o pipefail
set -o nounset
set -o xtrace

readonly LUSTRE_FS_NAME=${LUSTRE_FS_NAME:?Set LUSTRE_FS_NAME (e.g. lustrefs)}
readonly LUSTRE_MGS_IP=${LUSTRE_MGS_IP:?Set LUSTRE_MGS_IP (e.g. 10.1.0.7)}
readonly NAMESPACE=${NAMESPACE:-default}
readonly TIMEOUT=${TIMEOUT:-120s}
readonly SC_NAME="sc-unique-fsid-test"
readonly REPO_ROOT_PATH=${REPO_ROOT_PATH:-$(git rev-parse --show-toplevel)}
readonly TEST_POD_IMAGE=${TEST_POD_IMAGE:-alpine}

PASS=0
FAIL=0


pass() { echo "PASS: $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL: $1"; FAIL=$((FAIL + 1)); }

cleanup() {
    echo ""
    echo "=== Collecting CSI driver logs ==="
    bash "${REPO_ROOT_PATH}/utils/azurelustre_log.sh" 2>/dev/null || true
    echo "=== Cleaning up test resources (pods, then PVCs, then PVs) ==="
    for i in 1 2 3 4; do
        kubectl delete pod "fsid-test-pod-${i}" -n "$NAMESPACE" --ignore-not-found --grace-period=5 2>/dev/null || true
    done
    sleep 5
    for i in 1 2 3 4; do
        kubectl delete pvc "pvc-fsid-test-${i}" -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
    done
    for i in 1 2 3 4; do
        kubectl delete pv "pv-fsid-test-${i}" --ignore-not-found 2>/dev/null || true
    done
    kubectl delete storageclass "$SC_NAME" --ignore-not-found 2>/dev/null || true
    echo "=== Cleanup complete ==="
}
trap cleanup EXIT

get_node_name() {
    local pod_name=$1
    kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{.spec.nodeName}'
}

check_mount_options() {
    local node=$1
    local search=$2
    # Read host-level /proc/mounts via nsenter from the CSI node pod on the target node.
    # kubectl debug doesn't work reliably without a TTY in scripts.
    local csi_pod
    csi_pod=$(kubectl get pods -n kube-system -l app=csi-azurelustre-node \
        --field-selector "spec.nodeName=${node}" -o name | head -1)
    if [[ -z "$csi_pod" ]]; then
        echo ""
        return
    fi
    kubectl exec -n kube-system "${csi_pod}" -c azurelustre -- \
        nsenter -t 1 -m -- cat /proc/mounts 2>/dev/null | grep "$search" || true
}

get_fsid() {
    local pod_name=$1
    local fsid
    fsid=$(kubectl exec "$pod_name" -n "$NAMESPACE" -- \
        stat -f -c '%i' /mnt/lustre 2>/dev/null) || fsid=""
    if [[ -z "$fsid" ]]; then
        fail "Could not read f_fsid from ${pod_name}"
        return 1
    fi
    echo "$fsid"
}

# Pick a node to pin all test pods to
NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
echo "=== unique_fsid E2E tests ==="
echo "Lustre: ${LUSTRE_FS_NAME} @ ${LUSTRE_MGS_IP}"
echo "Node: ${NODE}"
echo ""

# --- Create StorageClass ---
kubectl apply -f - <<EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${SC_NAME}
provisioner: azurelustre.csi.azure.com
reclaimPolicy: Retain
volumeBindingMode: Immediate
mountOptions:
  - noatime
  - flock
EOF

# --- Test 1 & 2: Create two PVs with unique_fsid (auto) ---
for i in 1 2; do
    PV_YAML=$(cat <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: pv-fsid-test-${i}
spec:
  accessModes: [ReadWriteMany]
  capacity:
    storage: 4Ti
  csi:
    driver: azurelustre.csi.azure.com
    volumeAttributes:
      fs-name: ${LUSTRE_FS_NAME}
      mgs-ip-address: ${LUSTRE_MGS_IP}
      sub-dir: fsid-test-${i}
    volumeHandle: fsid-test-vol-${i}
  mountOptions: [noatime, flock]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ${SC_NAME}
EOF
    )
    echo "$PV_YAML" | kubectl apply -f -

    PVC_YAML=$(cat <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-fsid-test-${i}
  namespace: ${NAMESPACE}
spec:
  accessModes: [ReadWriteMany]
  resources:
    requests:
      storage: 4Ti
  storageClassName: ${SC_NAME}
  volumeName: pv-fsid-test-${i}
EOF
    )
    echo "$PVC_YAML" | kubectl apply -f -

    POD_YAML=$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: fsid-test-pod-${i}
  namespace: ${NAMESPACE}
spec:
  nodeName: ${NODE}
  containers:
  - name: test
    image: ${TEST_POD_IMAGE}
    command: [sh, -c, "while true; do sleep 3600; done"]
    volumeMounts:
    - mountPath: /mnt/lustre
      name: vol
  volumes:
  - name: vol
    persistentVolumeClaim:
      claimName: pvc-fsid-test-${i}
EOF
    )
    echo "$POD_YAML" | kubectl apply -f -
done

echo "Waiting for pods..."
kubectl wait --for=condition=ready pod/fsid-test-pod-1 pod/fsid-test-pod-2 \
    -n "$NAMESPACE" --timeout="$TIMEOUT"

# Test 1: Check unique_fsid in mount options
echo ""
echo "=== Test 1: unique_fsid auto-injection ==="
MOUNT_LINE=$(check_mount_options "$NODE" "fsid-test-1" | head -1)
if echo "$MOUNT_LINE" | grep -q "unique_fsid"; then
    pass "unique_fsid found in mount options"
else
    fail "unique_fsid NOT found in mount options: $MOUNT_LINE"
fi

# Test 2: Different f_fsid
echo ""
echo "=== Test 2: unique f_fsid per volume ==="
FSID1=$(get_fsid fsid-test-pod-1) || exit 1
FSID2=$(get_fsid fsid-test-pod-2) || exit 1
echo "  Volume 1 fsid: ${FSID1}"
echo "  Volume 2 fsid: ${FSID2}"
if [[ "$FSID1" != "$FSID2" ]]; then
    pass "Different f_fsid values"
else
    fail "Same f_fsid — unique_fsid may not be working"
fi

# --- Test 3: no_unique_fsid opt-out ---
echo ""
echo "=== Test 3: no_unique_fsid opt-out ==="
for i in 3 4; do
    PV_YAML=$(cat <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: pv-fsid-test-${i}
spec:
  accessModes: [ReadWriteMany]
  capacity:
    storage: 4Ti
  csi:
    driver: azurelustre.csi.azure.com
    volumeAttributes:
      fs-name: ${LUSTRE_FS_NAME}
      mgs-ip-address: ${LUSTRE_MGS_IP}
      sub-dir: fsid-test-${i}
    volumeHandle: fsid-test-vol-${i}
  mountOptions: [noatime, flock, no_unique_fsid]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: ${SC_NAME}
EOF
    )
    echo "$PV_YAML" | kubectl apply -f -

    PVC_YAML=$(cat <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-fsid-test-${i}
  namespace: ${NAMESPACE}
spec:
  accessModes: [ReadWriteMany]
  resources:
    requests:
      storage: 4Ti
  storageClassName: ${SC_NAME}
  volumeName: pv-fsid-test-${i}
EOF
    )
    echo "$PVC_YAML" | kubectl apply -f -

    POD_YAML=$(cat <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: fsid-test-pod-${i}
  namespace: ${NAMESPACE}
spec:
  nodeName: ${NODE}
  containers:
  - name: test
    image: ${TEST_POD_IMAGE}
    command: [sh, -c, "while true; do sleep 3600; done"]
    volumeMounts:
    - mountPath: /mnt/lustre
      name: vol
  volumes:
  - name: vol
    persistentVolumeClaim:
      claimName: pvc-fsid-test-${i}
EOF
    )
    echo "$POD_YAML" | kubectl apply -f -
done

kubectl wait --for=condition=ready pod/fsid-test-pod-3 pod/fsid-test-pod-4 \
    -n "$NAMESPACE" --timeout="$TIMEOUT"

FSID3=$(get_fsid fsid-test-pod-3) || exit 1
FSID4=$(get_fsid fsid-test-pod-4) || exit 1
echo "  Volume 3 fsid: ${FSID3}"
echo "  Volume 4 fsid: ${FSID4}"
if [[ "$FSID3" == "$FSID4" ]]; then
    pass "Same f_fsid with no_unique_fsid (shared behavior restored)"
else
    fail "Different f_fsid despite no_unique_fsid"
fi

# Check sentinel was stripped
MOUNT_LINE_3=$(check_mount_options "$NODE" "fsid-test-3" | head -1)
if echo "$MOUNT_LINE_3" | grep -q "no_unique_fsid"; then
    fail "no_unique_fsid sentinel leaked to kernel mount options"
else
    pass "no_unique_fsid sentinel correctly stripped"
fi

# --- Test 4: Unmount isolation ---
echo ""
echo "=== Test 4: unmount isolation ==="
kubectl delete pod fsid-test-pod-2 -n "$NAMESPACE" --grace-period=5
kubectl delete pvc pvc-fsid-test-2 -n "$NAMESPACE"
kubectl wait --for=delete pod/fsid-test-pod-2 -n "$NAMESPACE" --timeout="$TIMEOUT" 2>/dev/null || true
sleep 5

if kubectl exec fsid-test-pod-1 -n "$NAMESPACE" -- ls /mnt/lustre >/dev/null 2>&1; then
    pass "Volume 1 still accessible after unmounting volume 2"
else
    fail "Volume 1 broken after unmounting volume 2"
fi

if kubectl exec fsid-test-pod-3 -n "$NAMESPACE" -- ls /mnt/lustre >/dev/null 2>&1; then
    pass "Volume 3 (no_unique_fsid) still accessible after unmounting volume 2"
else
    fail "Volume 3 broken after unmounting volume 2"
fi

# --- Summary ---
echo ""
echo "==============================="
echo "  unique_fsid E2E results"
echo "  PASSED: ${PASS}"
echo "  FAILED: ${FAIL}"
echo "==============================="

if [[ $FAIL -gt 0 ]]; then
    exit 1
fi
