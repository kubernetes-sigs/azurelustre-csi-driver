#!/bin/bash

# Copyright 2022 The Kubernetes Authors.
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

#
# Shell script to install Lustre client kernel modules and launch CSI driver
#   $1 is the path to the CSI driver.
#

set -o xtrace
set -o errexit
set -o pipefail
set -o nounset

# Exit promptly if terminated during startup (package install, CA-trust refresh,
# module load) before the loader installs its module-unloading handler further
# down. As PID 1 the shell gets no default SIGTERM action, so without a handler
# the kubelet's SIGTERM is ignored until the grace period expires and a SIGKILL
# lands. Nothing is loaded to clean up this early, so just exit; the loader role
# upgrades this to a teardown handler once it owns loaded modules. NOTE: bash
# runs a trap only after the current foreground command returns, so a signal
# arriving deep inside a single long-running install command still waits for
# that command to finish; this handler bounds the delay for the gaps between
# install steps and the retry backoff sleeps.
trap 'echo "$(date -u) Termination signal received during startup; exiting."; exit 0' TERM INT

# add_net_interfaces discovers the host ethernet interfaces and adds any that
# are missing from the LNet tcp network. Pass "quiet" to suppress the per-cycle
# diagnostics (route table, per-interface decisions, "already added") for the
# steady-state reconcile loop; it still logs when it actually adds an interface
# or cannot find one. load_lnet calls it verbosely at startup.
#
# Returns non-zero when no ethernet interface is found; it must not exit. The
# reconcile loop calls it every cycle and tolerates a transient failure, so an
# exit here would take the whole sidecar down. The startup callers invoke it
# bare, so errexit still aborts the container there.
function add_net_interfaces() {
  local quiet="${1:-}"
  [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Determining ethernet interfaces."
  [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Route table is:"
  [[ "${quiet}" == "quiet" ]] || ip route list
  interface_list=$(ip route show | sed -n 's/.*\s\+dev\s\+\([^ ]\+\).*/\1/p' | sort -u)
  ethernet_interfaces=()
  for interface in ${interface_list}; do
    interface_info=$(ip link show "${interface}")
    if [[ "${interface_info}" =~ 'SLAVE' ]]; then
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Not adding slave interface: ${interface}"
    elif [[ "${interface_info}" =~ 'link-netns' ]]; then
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Not adding namespaced interface: ${interface}"
    elif [[ "${interface_info}" =~ 'UNKNOWN' ]]; then
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Not adding state unknown interface: ${interface}"
    elif [[ "${interface_info}" =~ 'link/ether' ]]; then
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Including ethernet interface: ${interface}"
      ethernet_interfaces+=("${interface}")
    else
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Skipping non-ethernet interface: ${interface}"
    fi
  done
  [[ "${quiet}" == "quiet" ]] || echo "$(date -u) List of found ethernet interfaces is: ${ethernet_interfaces[*]}"

  if [[ "${#ethernet_interfaces[@]}" -eq 0 ]]; then
    echo "$(date -u) Cannot find any ethernet network interface"
    return 1
  fi

  local tcp_net_show
  for interface in "${ethernet_interfaces[@]}"; do
    # timeout bounds a wedged lnetctl so it cannot defer the termination trap
    # (which runs teardown_lnet) past the pod's grace period. Capture then
    # match; "lnetctl ... | grep -q" can SIGPIPE the producer under
    # set -o pipefail and falsely report the interface absent.
    tcp_net_show=$(timeout 10 lnetctl net show --net tcp 2>/dev/null || true)
    if grep -qw "${interface}" <<<"${tcp_net_show}"; then
      [[ "${quiet}" == "quiet" ]] || echo "$(date -u) Interface already added, skipping: ${interface}"
    else
      echo "$(date -u) Adding interface: ${interface}"
      timeout 10 lnetctl net add --net tcp --if "${interface}"
    fi
  done
}

# load_lnet loads the LNet/Lustre client kernel modules and configures the LNet
# tcp network. It is idempotent and safe to call repeatedly. Because it issues
# modprobe, it runs once at loader startup and is also the path the sidecar's
# livenessProbe-driven restart relies on to (re)load a wedged module. It is
# NOT called from the steady-state reconcile loop (see reconcile_lnet).
function load_lnet() {
  local init_lnet="true"

  if [[ -d /sys/module/lnet ]]; then
    local tcp_net_show all_net_show
    tcp_net_show=$(timeout 10 lnetctl net show --net tcp 2>/dev/null || true)
    if grep -q interfaces <<<"${tcp_net_show}"; then
      echo "$(date -u) LNet is loaded skip the load"
      echo "$(date -u) Adding missing interfaces"
      add_net_interfaces
      init_lnet="false"
    else
      all_net_show=$(timeout 10 lnetctl net show 2>/dev/null || true)
      if grep -q "net type: tcp" <<<"${all_net_show}"; then
        # There may be a default configuration with no interface.
        # This is configured by an old version CSI.
        timeout 10 lnetctl net del --net tcp
      fi
    fi
  fi

  if [[ "${init_lnet}" == "true" ]]; then
    echo "$(date -u) Loading the LNet."
    modprobe -v lnet
    modprobe -v ksocklnd skip_mr_route_setup=1
    timeout 10 lnetctl lnet configure

    add_net_interfaces

    echo "$(date -u) Done"
  fi

  echo "$(date -u) Enabling Lustre client kernel modules."
  modprobe -v mgc
  modprobe -v lustre

  echo "$(date -u) Enabled Lustre client kernel modules."
}

# reconcile_lnet re-applies LNet *configuration* (tcp net + ethernet
# interfaces) if it has drifted while the node is running. It deliberately
# does NOT modprobe or reload kernel modules: reloading is owned by load_lnet
# at startup and by the kubelet restarting this sidecar when its livenessProbe
# fails. Keeping reload out of the loop avoids racing the termination-trap
# module unload (teardown_lnet) during pod termination.
function reconcile_lnet() {
  echo "$(date -u) Reconciling LNet interfaces."
  if [[ ! -d /sys/module/lnet ]]; then
    # The module is gone entirely; in-process config repair cannot help.
    # Leave recovery to the sidecar livenessProbe -> kubelet restart -> load_lnet.
    echo "$(date -u) LNet module not loaded; deferring recovery to sidecar restart."
    return 0
  fi

  local tcp_net_show
  tcp_net_show=$(timeout 10 lnetctl net show --net tcp 2>/dev/null || true)
  local rc=0
  if ! grep -q interfaces <<<"${tcp_net_show}"; then
    echo "$(date -u) LNet tcp network missing interfaces; reconfiguring."
    timeout 10 lnetctl lnet configure || rc=$?
  fi

  # Re-add any ethernet interfaces that have dropped out of the LNet config.
  # "quiet" keeps the steady-state loop to one line unless it actually repairs.
  add_net_interfaces quiet || rc=$?
  # A failing cycle is tolerated -- the loop retries -- but it must not look
  # like a success, so report it to the caller.
  return "${rc}"
}

# teardown_lnet unloads the Lustre/LNet kernel modules when the loader sidecar
# terminates so a driver upgrade does not leave stale modules resident. It runs
# from the SIGTERM trap, not a preStop hook, so it shares PID 1 with the reconcile
# loop and bash cannot run the two concurrently -- teardown cannot race the loop
# re-adding the NI. The trap is armed before modules load, hence the /sys/module
# guard. lustre_rmmod alone suffices: it unloads the stack in dependency order and
# drops LNet (its "lnetctl lnet unconfigure" is gated on "which", which the image
# ships).
function teardown_lnet() {
  if [[ ! -d /sys/module/lnet ]]; then
    echo "$(date -u) No Lustre kernel modules loaded; nothing to unload on teardown."
    return 0
  fi

  echo "$(date -u) Unloading Lustre client kernel modules on teardown."
  if timeout 15 lustre_rmmod; then
    echo "$(date -u) Lustre client kernel modules unloaded on teardown."
  else
    echo "$(date -u) WARNING: Lustre kernel modules could not be unloaded and remain loaded on this node, most likely because a Lustre filesystem is still mounted."
  fi
}

# ---- OS / environment helpers ----

# detect_os_family sets osFamily ("azurelinux" or "ubuntu") from the container's
# /etc/os-release, or exits if the OS is unsupported.
function detect_os_family() {
  local osID=""
  osFamily=""
  if [[ -f /etc/os-release ]]; then
    # shellcheck disable=SC1091 # /etc/os-release is a runtime file
    osID=$(. /etc/os-release; echo "${ID:-}")
    if [[ "${osID}" == "azurelinux" || "${osID}" == "mariner" ]]; then
      osFamily="azurelinux"
    elif [[ "${osID}" == "ubuntu" || "${osID}" == "debian" ]]; then
      osFamily="ubuntu"
    fi
  fi
  if [[ -z "${osFamily}" ]]; then
    echo "$(date -u) Error: Unsupported container OS: ${osID:-unknown}. Supported: ubuntu, debian, azurelinux, mariner."
    exit 1
  fi
  echo "$(date -u) Detected OS family: ${osFamily}"
}

# update_ca_trust refreshes the CA store so outbound HTTPS works for every role
# (package installs from packages.microsoft.com and the controller's Azure calls).
function update_ca_trust() {
  if [[ "${osFamily}" == "azurelinux" ]]; then
    update-ca-trust
  else
    update-ca-certificates
  fi
}

# validate_client_version requires LUSTRE_VERSION and CLIENT_SHA_SUFFIX and sets
# the OS-appropriate pkgVersion and pkgName used to build package specs.
function validate_client_version() {
  if [[ -z "${LUSTRE_VERSION:-}" ]]; then
    echo "LUSTRE_VERSION environment variable is not set"
    exit 1
  fi
  echo "requiredLustreVersion: ${LUSTRE_VERSION}"

  if [[ -z "${CLIENT_SHA_SUFFIX:-}" ]]; then
    echo "CLIENT_SHA_SUFFIX environment variable is not set"
    exit 1
  fi
  echo "requiredClientSha: ${CLIENT_SHA_SUFFIX}"

  if [[ "${osFamily}" == "azurelinux" ]]; then
    # Azure Linux RPM versions are underscore-separated and identical to the
    # kernel module's own version string: 2.16.1_21_g153e389
    pkgVersion="$(wanted_module_version)"
  else
    # Ubuntu deb packages use dashes: 2.15.7-33-g79ddf99
    pkgVersion="${LUSTRE_VERSION}-${CLIENT_SHA_SUFFIX}"
  fi
  echo "pkgVersion: ${pkgVersion}"

  pkgName="amlfs-lustre-client-${pkgVersion}"
  echo "pkgName: ${pkgName}"
}

# wanted_module_version prints the version the loaded lustre module is expected
# to report. /sys/module/lustre/version is underscore-separated on every flavor,
# including Ubuntu, whose *package* version uses dashes.
function wanted_module_version() {
  echo "${LUSTRE_VERSION}_${CLIENT_SHA_SUFFIX//-/_}"
}

# loaded_lustre_version prints the running lustre module's version, or nothing
# when no module is loaded. sysfs is the only source available before the client
# package (and therefore lctl) is installed.
function loaded_lustre_version() {
  if [[ -r /sys/module/lustre/version ]]; then
    cat /sys/module/lustre/version
  fi
}

# lustre_module_refcnt prints the running lustre module's reference count, or
# "unknown". Non-zero is why the kernel refuses to unload the module.
function lustre_module_refcnt() {
  if [[ -r /sys/module/lustre/refcnt ]]; then
    cat /sys/module/lustre/refcnt
  else
    echo "unknown"
  fi
}

# check_loaded_module_version warns when the running kernel module is not the
# version this container installed, which means mounts on this node use the
# resident module rather than the one just installed.
function check_loaded_module_version() {
  local loaded wanted
  loaded="$(loaded_lustre_version)"
  wanted="$(wanted_module_version)"
  if [[ -z "${loaded}" ]]; then
    echo "$(date -u) WARNING: cannot read /sys/module/lustre/version, so the running Lustre client version cannot be confirmed."
  elif [[ "${loaded}" != "${wanted}" ]]; then
    echo "$(date -u) WARNING: the Lustre client upgrade did not take effect on this node. The running kernel module is ${loaded}, but this driver installed ${wanted}, most likely because a Lustre filesystem was still mounted when the previous node pod shut down. Mounts on this node continue to use ${loaded}."
  else
    echo "$(date -u) Running Lustre kernel module version ${loaded} matches the installed client."
  fi
}

# ---- package-repo setup (per OS) ----

# setup_pmc_repo validates host/container OS compatibility and adds the Microsoft
# package repository for the detected OS family.
function setup_pmc_repo() {
  if [[ "${osFamily}" == "azurelinux" ]]; then
    setup_pmc_repo_azurelinux
  else
    setup_pmc_repo_ubuntu
  fi
}

function setup_pmc_repo_azurelinux() {
  local containerVersionID hostID hostVersionID
  # shellcheck disable=SC1091 # /etc/os-release is a runtime file
  containerVersionID=$(. /etc/os-release; echo "${VERSION_ID:-}")
  # shellcheck disable=SC1091 # /etc/host-os-release is mounted at runtime
  # shellcheck disable=SC2031 # intentional: read ID in a subshell to avoid polluting the current scope
  hostID=$(. /etc/host-os-release; echo "${ID:-}")
  # shellcheck disable=SC1091 # /etc/host-os-release is mounted at runtime
  # shellcheck disable=SC2031 # intentional: read VERSION_ID in a subshell to avoid polluting the current scope
  hostVersionID=$(. /etc/host-os-release; echo "${VERSION_ID:-}")
  if [[ -z "${containerVersionID}" ]]; then
    echo "Could not determine container OS VERSION_ID from /etc/os-release"
    exit 1
  fi
  if [[ -z "${hostVersionID}" ]]; then
    echo "Could not determine host OS VERSION_ID from /etc/host-os-release"
    exit 1
  fi
  if [[ "${hostID}" != "azurelinux" && "${hostID}" != "mariner" ]]; then
    echo "Incompatible host OS detected: ${hostID}, expected: azurelinux"
    exit 1
  fi
  # Compare major version (e.g., "3" from "3.0" or "3.0.20260517")
  if [[ "${hostVersionID%%.*}" != "${containerVersionID%%.*}" ]]; then
    echo "Incompatible host OS version: ${hostVersionID}, expected major version: ${containerVersionID%%.*}"
    exit 1
  fi

  echo "$(date -u) Adding Microsoft package repository for Lustre client modules."
  rpm --import https://packages.microsoft.com/keys/microsoft.asc
  cat > /etc/yum.repos.d/amlfs.repo <<REPOEOF
[amlfs]
name=Azure Lustre Packages
baseurl=https://packages.microsoft.com/yumrepos/amlfs-al${containerVersionID%%.*}
enabled=1
gpgcheck=1
gpgkey=https://packages.microsoft.com/keys/microsoft.asc
REPOEOF
}

function setup_pmc_repo_ubuntu() {
  local osReleaseCodeName hostCodeName ARCH
  # shellcheck disable=SC1091,SC2154 # /etc/os-release sets VERSION_CODENAME
  osReleaseCodeName=$(. /etc/os-release; echo "${VERSION_CODENAME}")
  if [[ -z "${osReleaseCodeName}" ]]; then
    echo "Could not determine OS release codename"
    exit 1
  fi

  # shellcheck disable=SC1091 # /etc/host-os-release exists only at runtime inside the container
  if ! grep -q -R "${osReleaseCodeName}" /etc/host-os-release; then
    # shellcheck disable=SC2031 # intentional: read VERSION_CODENAME in a subshell to avoid polluting the current scope
    hostCodeName=$(. /etc/host-os-release; echo "${VERSION_CODENAME}")
    if [[ "${hostCodeName}" == "focal" && "${osReleaseCodeName}" == "jammy" ]]; then
      echo "Allowing jammy container on focal host, this usage is deprecated and will be removed in future"
    else
      echo "Incompatible host OS detected: ${hostCodeName}, expected: ${osReleaseCodeName}"
      exit 1
    fi
  fi

  ARCH=$(uname -m)
  if [[ "${ARCH}" != "x86_64" && "${ARCH}" != "aarch64" ]]; then
    echo "$(date -u) Error: Unsupported architecture: ${ARCH}"
    exit 1
  fi
  if [[ "${ARCH}" == "x86_64" ]]; then
    ARCH="amd64"
  else
    ARCH="arm64"
  fi

  # Prefer the Ubuntu CDN; keep the Azure mirror as a lower-priority apt fallback.
  /app/useAzureAptMirror.sh

  echo "$(date -u) Adding Microsoft package repository for Lustre client modules, architecture=${ARCH}."
  curl -sL https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor | tee /etc/apt/trusted.gpg.d/microsoft.gpg > /dev/null
  echo "deb [arch=${ARCH}] https://packages.microsoft.com/repos/amlfs-${osReleaseCodeName}/ ${osReleaseCodeName} main" | tee /etc/apt/sources.list.d/amlfs.list
  apt-get update
}

# ---- package install (per OS) ----

# full_package_spec prints the kernel-pinned metapackage (kmod + kernel + utils)
# that the loader installs.
function full_package_spec() {
  local kernelVersion
  kernelVersion=$(uname -r)
  if [[ "${osFamily}" == "azurelinux" ]]; then
    # Transform kernel version for RPM package name:
    # e.g. 6.6.139.1-1.azl3.x86_64 -> 6.6.139.1.1.azl3
    local kernelPkgVersion
    kernelPkgVersion=$(echo "${kernelVersion}" | sed -e "s/\.$(uname -m)$//" | sed -re 's/[-_]/\./g')
    echo "${pkgName}-${kernelPkgVersion}"
  else
    echo "${pkgName}=${kernelVersion}"
  fi
}

# utils_package_spec prints the kernel-agnostic userspace utils package(s) that
# the driver installs. On Ubuntu the deb does not declare its libnl/libyaml
# runtime deps (needed by lnetctl), so they are named explicitly.
function utils_package_spec() {
  if [[ "${osFamily}" == "azurelinux" ]]; then
    echo "lustre-client-${pkgVersion}"
  else
    echo "lustre-client-${pkgVersion} libnl-3-200 libnl-genl-3-200 libyaml-0-2"
  fi
}

# prepare_depmod_stub creates the module dir and the index files the Ubuntu
# lustre-client deb's postinst `depmod -a` expects. The driver container ships no
# kernel package, so without the dir depmod exits non-zero and fails the
# postinst; without the index files it succeeds but warns on every install. Empty
# files are accurate for this container, which has no kernel modules.
function prepare_depmod_stub() {
  if [[ "${osFamily}" == "ubuntu" ]]; then
    local moddir
    moddir="/lib/modules/$(uname -r)"
    mkdir -p "${moddir}"
    touch "${moddir}/modules.order" "${moddir}/modules.builtin" "${moddir}/modules.builtin.modinfo"
  fi
}

# install_pkg runs the OS-native package installer for the given package(s).
function install_pkg() {
  if [[ "${osFamily}" == "azurelinux" ]]; then
    tdnf install -y --disablerepo="*" --enablerepo="amlfs" --enablerepo="azurelinux-official-base" "$@"
  else
    # grub issue
    # https://stackoverflow.com/questions/40748363/virtual-machine-apt-get-grub-issue/40751712
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends -o DPkg::options::="--force-confdef" -o DPkg::options::="--force-confold" "$@"
  fi
}

# evict_old_lustre is the loader's on-failure hook: it removes existing Lustre
# client packages so a retry can install cleanly, and unloads the running
# modules only when their version differs from the one being installed. The
# driver never calls this -- the modules belong to the kernel and were loaded by
# the loader sidecar.
function evict_old_lustre() {
  local -a existing_pkgs=()
  local existing_versions=""
  local loaded
  echo "$(date -u) Will try removing existing versions"
  # Unloading is destructive and the install may have failed for a reason that
  # has nothing to do with the modules (an unreachable repo, say), so a module
  # that already matches the requested version is left running.
  loaded="$(loaded_lustre_version)"
  if [[ -z "${loaded}" ]]; then
    echo "$(date -u) No Lustre kernel module is loaded; nothing to unload."
  elif [[ "${loaded}" == "$(wanted_module_version)" ]]; then
    echo "$(date -u) Loaded Lustre kernel module ${loaded} already matches the requested version; leaving it loaded."
  elif ! type lustre_rmmod >/dev/null 2>&1; then
    echo "$(date -u) WARNING: loaded Lustre kernel module ${loaded} does not match the requested version, but lustre_rmmod is not installed so it cannot be unloaded; the old client will keep running."
  elif ! lustre_rmmod; then
    echo "$(date -u) WARNING: could not unload Lustre kernel module ${loaded} (module refcnt $(lustre_module_refcnt)); the old client will keep running."
  fi
  if [[ "${osFamily}" == "azurelinux" ]]; then
    mapfile -t existing_pkgs < <(rpm -qa '*lustre-client*' 2>/dev/null || true)
    if [[ ${#existing_pkgs[@]} -gt 0 ]]; then
      echo "$(date -u) The following existing versions of the Lustre client are installed and will be removed: ${existing_pkgs[*]}"
      echo "$(date -u) Uninstalling existing Lustre client versions."
      tdnf remove -y "${existing_pkgs[@]}" || true
    fi
  else
    if existing_versions=$(dpkg-query --showformat=' ${Package}=${Version}' --show '*lustre-client*'); then
      echo  "$(date -u) The following existing versions of the Lustre client are installed and will be removed:${existing_versions}"
    fi
    echo "$(date -u) Uninstalling existing Lustre client versions."
    apt-get remove --purge -y '*lustre-client*' || true
  fi
}

# retry_install runs an install command with bounded retries and exponential
# backoff. On each failure it invokes the named on-failure hook (if any) before
# retrying. Returns non-zero if all attempts fail.
#   $1     - name of on-failure hook function, or "" for none
#   $2...  - install command and arguments
function retry_install() {
  local on_failure="$1"
  shift
  local tries=3
  local sleep_before_retry=15
  while [[ tries -gt 0 ]]; do
    if "$@"; then
      return 0
    fi
    tries=$((tries - 1))
    echo "$(date -u) Error installing Lustre client packages. Tries left: ${tries}."
    if [[ -n "${on_failure}" ]]; then
      "${on_failure}"
    fi
    if [[ tries -gt 0 ]]; then
      sleep "${sleep_before_retry}"
      sleep_before_retry=$((sleep_before_retry * 2))
    fi
  done
  return 1
}

# install_lustre_client installs the package set for the role and logs the
# outcome. It returns non-zero if the install could not be completed, and both
# roles call it bare so errexit aborts the container: without the client
# packages the loader cannot load the modules and the driver has no
# mount.lustre, so opening the CSI socket anyway would advertise the node as
# able to serve mounts that are guaranteed to fail. Exiting instead lets the
# kubelet restart the container and retry.
#   $1 - "full" (loader: kmod+kernel+utils) or "utils" (driver: userspace only)
function install_lustre_client() {
  local kind="$1"
  validate_client_version
  setup_pmc_repo

  local -a spec
  local on_failure=""
  if [[ "${kind}" == "full" ]]; then
    read -r -a spec <<<"$(full_package_spec)"
    on_failure="evict_old_lustre"
  else
    prepare_depmod_stub
    read -r -a spec <<<"$(utils_package_spec)"
  fi

  echo "$(date -u) Installing Lustre client packages for OS=${osFamily}, kernel=$(uname -r): ${spec[*]}"
  if ! retry_install "${on_failure}" install_pkg "${spec[@]}"; then
    echo "$(date -u) Error: Could not install necessary Lustre packages for: ${spec[*]}"
    echo "$(date -u) Note: the package manager cannot distinguish an unavailable package version from an unreachable package repository -- a 'package not found' above can mean either. Check connectivity to packages.microsoft.com before concluding the version is wrong."
    return 1
  fi
  echo "$(date -u) Installed Lustre client packages for: ${spec[*]}"
}

# ---- per-role flows ----

# exec_csi_driver replaces the shell with the CSI driver binary passed as "$@".
function exec_csi_driver() {
  echo "$(date -u) Entering Lustre CSI driver"
  if [[ $# -eq 0 ]]; then
    echo "$(date -u) Error: No command provided to execute. Usage: entrypoint.sh <csi-driver-binary> [args...]"
    exit 1
  fi
  echo "Executing: $*"
  exec "$@"
}

# run_loader installs the full client, loads the kernel modules + LNet, then
# reconciles LNet configuration for the life of the pod. It never execs the
# CSI driver.
function run_loader() {
  install_lustre_client "full"

  # Upgrade the startup termination handler (which only exits) to one that also
  # unloads the kernel modules, now that this sidecar owns them. Set BEFORE
  # load_lnet so a SIGTERM during module load unloads cleanly too.
  trap 'echo "$(date -u) Termination signal received; unloading LNet and exiting."; teardown_lnet || true; exit 0' TERM INT
  load_lnet
  check_loaded_module_version
  echo "$(date -u) Loader ready; entering LNet reconcile loop (interval ${RECONCILE_INTERVAL_SECONDS}s)."
  # Quiet xtrace for the steady-state loop; reconcile_lnet logs one line per
  # cycle and full detail only when it actually repairs.
  set +o xtrace
  while true; do
    # Background the sleep and wait on it: bash defers a trap until the current
    # foreground command returns, so a bare "sleep" would delay the trap (and
    # thus teardown) by up to one interval. A signal interrupts "wait"
    # immediately, runs the trap, and exits in well under a second.
    sleep "${RECONCILE_INTERVAL_SECONDS}" &
    wait $! || true
    reconcile_lnet || echo "$(date -u) LNet reconciliation did not complete this cycle; will retry in ${RECONCILE_INTERVAL_SECONDS}s."
  done
  # Not reached.
}

# run_driver installs only the kernel-agnostic userspace utils, then execs the
# CSI driver. It relies on the loader sidecar having already loaded the kernel
# modules into the shared host kernel.
function run_driver() {
  install_lustre_client "utils"
  exec_csi_driver "$@"
}

# run_controller does no kernel-module work; it just execs the CSI driver.
function run_controller() {
  exec_csi_driver "$@"
}

# ---- dispatch ----

detect_os_family

update_ca_trust

# RECONCILE_INTERVAL_SECONDS controls how often the loader sidecar re-checks
# and re-applies LNet configuration. Overridable via env for testing.
RECONCILE_INTERVAL_SECONDS="${AZURELUSTRE_CSI_LNET_RECONCILE_INTERVAL:-30}"

# Role is REQUIRED; an unset or unknown value is a deployment error, not a silent
# no-op. See run_loader / run_driver / run_controller for what each one does.
role="${AZURELUSTRE_CSI_ROLE:-}"
echo "role: ${role:-<unset>}"
echo "$(date -u) Command line arguments: $*"
case "${role}" in
  loader)     run_loader ;;
  driver)     run_driver "$@" ;;
  controller) run_controller "$@" ;;
  *)
    echo "$(date -u) Error: AZURELUSTRE_CSI_ROLE must be one of loader, driver, controller (got '${role}')."
    exit 1
    ;;
esac
