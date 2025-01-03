import fcntl
import json
import logging
import os
import socket
import struct
import subprocess
import sys
import threading
import time
import re
from dataclasses import dataclass
from functools import lru_cache, partial
from os.path import exists
from textwrap import dedent
from typing import List, Optional, Tuple

MODE = os.environ.get("MODE")
assert MODE != ""
NUM_CORES = int(os.environ.get("CORES", 0))
CORE_IDS = os.environ.get("CORE_IDS", "auto")
NAME = os.environ["NAME"]
NETWORK_DEVICE = os.environ.get("NETWORK_DEVICE", "")
PORT = os.environ.get("PORT", "")
AGENT_PORT = os.environ.get("AGENT_PORT", "")
RESOURCES = {}  # to be populated at later stage
MEMORY = os.environ.get("MEMORY", "")
JOIN_IPS = os.environ.get("JOIN_IPS", "")
DIST_SERVICE = os.environ.get("DIST_SERVICE")
OS_DISTRO = ""
OS_BUILD_ID = ""
DISCOVERY_SCHEMA = 1
INSTRUCTIONS = os.environ.get("INSTRUCTIONS", "")
NODE_NAME = os.environ["NODE_NAME"]
FAILURE_DOMAIN_LABEL = os.environ.get("FAILURE_DOMAIN_LABEL", "")
FAILURE_DOMAIN = os.environ.get("FAILURE_DOMAIN", None)
MACHINE_IDENTIFIER = os.environ.get("MACHINE_IDENTIFIER", None)
NET_GATEWAY=os.environ.get("NET_GATEWAY", None)

KUBERNETES_DISTRO_OPENSHIFT = "openshift"
KUBERNETES_DISTRO_GKE = "gke"
OS_NAME_GOOGLE_COS = "cos"
OS_NAME_REDHAT_COREOS = "rhcos"

MAX_TRACE_CAPACITY_GB = os.environ.get("MAX_TRACE_CAPACITY_GB", 10)
ENSURE_FREE_SPACE_GB = os.environ.get("ENSURE_FREE_SPACE_GB", 20)

WEKA_CONTAINER_ID = os.environ.get("WEKA_CONTAINER_ID", "")
WEKA_PERSISTENCE_DIR = os.environ.get("WEKA_PERSISTENCE_DIR")
WEKA_PERSISTENCE_MODE = os.environ.get("WEKA_PERSISTENCE_MODE", "local")
WEKA_PERSISTENCE_GLOBAL_DIR = "/opt/weka-global-persistence"
if WEKA_PERSISTENCE_MODE == "global":
    WEKA_PERSISTENCE_DIR = os.path.join(WEKA_PERSISTENCE_GLOBAL_DIR, "containers", WEKA_CONTAINER_ID)


WEKA_COS_ALLOW_HUGEPAGE_CONFIG = True if os.environ.get("WEKA_COS_ALLOW_HUGEPAGE_CONFIG", "false") == "true" else False
WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING = True if os.environ.get("WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING",
                                                               "false") == "true" else False
WEKA_COS_GLOBAL_HUGEPAGE_SIZE = os.environ.get("WEKA_COS_GLOBAL_HUGEPAGE_SIZE", "2M").lower()
WEKA_COS_GLOBAL_HUGEPAGE_COUNT = int(os.environ.get("WEKA_COS_GLOBAL_HUGEPAGE_COUNT", 4000))


AWS_VENDOR_ID = "1d0f"
AWS_DEVICE_ID = "cd01"

# for client dynamic port allocation
BASE_PORT = os.environ.get("BASE_PORT", "")
PORT_RANGE = os.environ.get("PORT_RANGE", "0")
WEKA_CONTAINER_PORT_SUBRANGE = 100
MAX_PORT = 65535

# Define global variables
exiting = 0

# Formatter with channel name
formatter = logging.Formatter('%(asctime)s - %(name)s - %(levelname)s - %(message)s')

# Define handlers for stdout and stderr
stdout_handler = logging.StreamHandler(sys.stdout)
stdout_handler.setLevel(logging.DEBUG)
stderr_handler = logging.StreamHandler(sys.stderr)
stderr_handler.setLevel(logging.WARNING)

# Basic configuration
logging.basicConfig(
    level=logging.DEBUG,  # Global minimum logging level
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',  # Include timestamp
    handlers=[stdout_handler, stderr_handler]
)


async def sign_drives_by_pci_info(vendor_id: str, device_id: str) -> List[str]:
    logging.info("Signing drives. Vendor ID: %s, Device ID: %s", vendor_id, device_id)

    if not vendor_id or not device_id:
        raise ValueError("Vendor ID and Device ID are required")

    cmd = f"lspci -d {vendor_id}:{device_id}" + " | sort | awk '{print $1}'"
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        return

    signed_drives = []
    pci_devices = stdout.decode().strip().split()
    for pci_device in pci_devices:
        device = f"/dev/disk/by-path/pci-0000:{pci_device}-nvme-1"
        stdout, stderr, ec = await run_command(f"weka local exec -- /weka/tools/weka_sign_drive {device}")
        if ec != 0:
            logging.error(f"Failed to sign drive {pci_device}: {stderr}")
            continue
        signed_drives.append(device)
        # TODO: Find serial id already here? it is adhocy non mandatory operation, so does not make sense to populate some db at this point, despite us having info
    return signed_drives


async def sign_not_mounted() -> List[str]:
    """
    [root@wekabox18 sdc] 2024-08-21 19:14:58 $ cat /run/udev/data/b`cat /sys/block/sdc/dev` | grep ID_SERIAL=
        E:ID_SERIAL=TOSHIBA_THNSN81Q92CSE_86DS107ATB4V
    :return:
    """
    logging.info("Signing drives not mounted")
    stdout, stderr, ec = await run_command("lsblk -o NAME,TYPE,MOUNTPOINT", capture_stdout=True)
    if ec != 0:
        return
    lines = stdout.decode().strip().split("\n")
    signed_drives = []
    logging.info(f"Found drives: {lines}")
    for line in lines[1:]:
        parts = line.split()
        logging.info(f"Processing line: {parts}")
        if parts[1] == "disk" and len(parts) < 3:
            device = f"/dev/{parts[0]}"
            logging.info(f"Signing drive {device}")
            stdout, stderr, ec = await run_command(f"weka local exec -- /weka/tools/weka_sign_drive {device}")
            if ec != 0:
                logging.error(f"Failed to sign drive {device}: {stderr}")
                continue
            signed_drives.append(device)
    return signed_drives


async def sign_device_paths(devices_paths) -> List[str]:
    signed_drives = []
    for device_path in devices_paths:
        stdout, stderr, ec = await run_command(f"weka local exec -- /weka/tools/weka_sign_drive {device_path}")
        if ec != 0:
            logging.error(f"Failed to sign drive {device_path}: {stderr}")
            continue
        signed_drives.append(device_path)
    return signed_drives


async def sign_drives(instruction: dict) -> List[str]:
    type = instruction['type']
    if type == "aws-all":
        return await sign_drives_by_pci_info(
            vendor_id=AWS_VENDOR_ID,
            device_id=AWS_DEVICE_ID,
        )
    elif type == "device-identifiers":
        return await sign_drives_by_pci_info(
            vendor_id=instruction.get('pciDevices', {}).get('vendorId'),
            device_id=instruction.get('pciDevices', {}).get('deviceId'),
        )
    elif type == "all-not-root":
        return await sign_not_mounted()
    elif type == "device-paths":
        return await sign_device_paths(instruction['devicePaths'])
    else:
        raise ValueError(f"Unknown instruction type: {type}")


async def force_resign_drives_by_paths(devices_paths: List[str]):
    logging.info("Force resigning drives by paths: %s", devices_paths)
    signed_drives = []
    for device_path in devices_paths:
        stdout, stderr, ec = await run_command(f"weka local exec -- /weka/tools/weka_sign_drive --force {device_path}")
        if ec != 0:
            logging.error(f"Failed to sign drive {device_path}: {stderr}")
            continue
        signed_drives.append(device_path)
    write_results(dict(
        err=None,
        drives=signed_drives
    ))


async def force_resign_drives_by_serials(serials: List[str]):
    logging.info("Force resigning drives by serials: %s", serials)
    device_paths = []
    for serial in serials:
        device_path = await get_block_device_path_by_serial(serial)
        device_paths.append(device_path)

    await force_resign_drives_by_paths(device_paths)


async def get_block_device_path_by_serial(serial: str):
    logging.info(f"Getting block device path by serial {serial}")
    stdout, stderr, ec = await run_command("lsblk -no PATH | grep -m 1 $(basename $(ls -la /dev/disk/by-id/ | grep -m 1 " + serial + " | awk '{print $NF}'))")
    if ec != 0:
        logging.error(f"Failed to get block device path by serial {serial}: {stderr}")
        return
    device_path = stdout.decode().strip()
    return device_path


async def discover_drives():
    drives = await find_weka_drives()
    write_results(dict(
        err=None,
        drives=drives,
    ))


async def find_weka_drives():
    drives = []
    # ls /dev/disk/by-path/pci-0000\:03\:00.0-scsi-0\:0\:3\:0  | ssd

    devices = subprocess.check_output("ls /dev/disk/by-path/", shell=True).decode().strip().split()
    logging.info(f"All found in kernel block devices: {devices}")
    for block_device in devices:
        try:
            type_id = subprocess.check_output(f"blkid -s PART_ENTRY_TYPE -o value -p /dev/disk/by-path/{block_device}",
                                              shell=True).decode().strip()
        except subprocess.CalledProcessError:
            logging.error(f"Failed to get PART_ENTRY_TYPE for {block_device}")
            continue

        if type_id == "993ec906-b4e2-11e7-a205-a0a8cd3ea1de":
            # TODO: Read and populate actual weka guid here
            weka_guid = ""
            # resolve block_device to serial id
            partition_name = subprocess.check_output(f"basename $(readlink -f /dev/disk/by-path/{block_device})",
                                                     shell=True).decode().strip()
            pci_device_path = subprocess.check_output(f"readlink -f /sys/class/block/{partition_name}",
                                                      shell=True).decode().strip()
            if "nvme" in block_device:
                # 3 directories up is the serial id
                serial_id_path = "/".join(pci_device_path.split("/")[:-2]) + "/serial"
                serial_id = subprocess.check_output(f"cat {serial_id_path}", shell=True).decode().strip()
                device_path = "/dev/" + pci_device_path.split("/")[-2]
            else:
                device_name = pci_device_path.split("/")[-2]
                device_path = "/dev/" + device_name
                dev_index = subprocess.check_output(f"cat /sys/block/{device_name}/dev", shell=True).decode().strip()
                serial_id_cmd = f"cat /host/run/udev/data/b{dev_index} | grep ID_SERIAL="
                serial_id = subprocess.check_output(serial_id_cmd, shell=True).decode().strip().split("=")[-1]

            drives.append({
                "partition": "/dev/" + partition_name,
                "block_device": device_path,
                "serial_id": serial_id,
                "weka_guid": weka_guid
            })

    return drives


def is_google_cos():
    return OS_DISTRO == OS_NAME_GOOGLE_COS


def is_rhcos():
    return OS_DISTRO == OS_NAME_REDHAT_COREOS


def wait_for_syslog():
    while not os.path.isfile('/var/run/syslog-ng.pid'):
        time.sleep(0.1)
        print("Waiting for syslog-ng to start")


def wait_for_agent():
    while not os.path.isfile('/var/run/weka-agent.pid'):
        time.sleep(1)
        print("Waiting for weka-agent to start")


async def ensure_drivers():
    logging.info("waiting for drivers")
    drivers = "wekafsio wekafsgw mpin_user".split()
    if not is_google_cos():
        drivers.append("igb_uio")
        if version_params.get('uio_pci_generic') is not False:
            drivers.append("uio_pci_generic")
    driver_mode = await is_legacy_driver_cmd()
    logging.info(f"validating drivers in mode {MODE}, driver mode: {driver_mode}")
    # FORCING LEGACY MODE due to bug in drivers ready
    if False and not await is_legacy_driver_cmd() and MODE in ["client", "s3", "nfs"]: # we are not using legacy driver on backends, as it should not be validating specific versions, so just lsmoding
        while not exiting:
            version = await get_weka_version()
            stdout, stderr, ec = await run_command(f"weka driver ready --without-agent --version {version}")
            if ec != 0:
                with open("/tmp/weka-drivers.log_tmp", "w") as f:
                    f.write("weka-drivers-loading")
                    logging.warning(f"Drivers are not loaded, waiting for them")
                os.rename("/tmp/weka-drivers.log_tmp", "/tmp/weka-drivers.log")
                logging.error(f"Failed to validate drivers {stderr.decode('utf-8')}: exc={ec}")
                await asyncio.sleep(1)
                continue
            logging.info("drivers are ready")
            break
    else:
        for driver in drivers:
            while True:
                stdout, stderr, ec = await run_command(f"lsmod | grep -w {driver}")
                if ec == 0:
                    break
                # write driver name into /tmp/weka-drivers.log
                logging.info(f"Driver {driver} not loaded, waiting for it")
                with open("/tmp/weka-drivers.log_tmp", "w") as f:
                    logging.warning(f"Driver {driver} not loaded, waiting for it")
                    f.write(driver)
                os.rename("/tmp/weka-drivers.log_tmp", "/tmp/weka-drivers.log")
                await asyncio.sleep(1)
                continue

    with open("/tmp/weka-drivers.log_tmp", "w") as f:
        f.write("")
    os.rename("/tmp/weka-drivers.log_tmp", "/tmp/weka-drivers.log")
    logging.info("All drivers loaded successfully")


# This atrocities should be replaced by new weka driver build/publish/download/install functionality
VERSION_TO_DRIVERS_MAP_WEKAFS = {
    "4.3.1.29791-9f57657d1fb70e71a3fb914ff7d75eee-dev": dict(
        wekafs="cc9937c66eb1d0be-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev3": dict(
        wekafs="1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev4": dict(
        wekafs="1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.560-842278e2dca9375f84bd3784a4e7515c-dev5": dict(
        wekafs="1acd22f9ddbda67d-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.3.28-k8s-alpha-dev": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.3.28-k8s-alpha-dev2": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.3.28-k8s-alpha-dev3": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev2": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.3.2.783-f5fe2ec58286d9fa8fc033f920e6c842-dev3": dict(
        wekafs="1cb1639d52a2b9ca-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="6b519d501ea82063",
    ),
    "4.2.7.64-k8so-beta.10": dict(
        wekafs="1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
    ),
    "4.2.10.1693-251d3172589e79bd4960da8031a9a693-dev": dict(  # dev 4.2.7-based version
        wekafs="1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
    ),
    "4.2.10.1290-e552f99e92504c69126da70e1740f6e4-dev": dict(
        wekafs="1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
    ),
    "4.2.10-k8so.0": dict(
        wekafs="1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
    ),
    "4.2.10.1671-363e1e8fcfb1290e061815445e973310-dev": dict(
        wekafs="1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
    ),
    "4.3.3": dict(
        wekafs="cbd05f716a3975f7-GW_556972ab1ad2a29b0db5451e9db18748",
        uio_pci_generic=False,
        dependencies="7955984e4bce9d8b",
        weka_drivers_handling=False,
    ),
}
# WEKA_DRIVER_VERSION_OPTIONS = [
#     "1.0.0-c50570e208c935e9129c9054140ab11a-GW_aedf44a11ca66c7bb599f302ae1dff86",
#     "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
# ]
IGB_UIO_DRIVER_VERSION = "weka1.0.2"
MPIN_USER_DRIVER_VERSION = "1.0.1"
UIO_PCI_GENERIC_DRIVER_VERSION = "5f49bb7dc1b5d192fb01b442b17ddc0451313ea2"
DEFAULT_DEPENDENCY_VERSION = "1.0.0-024f0fdaa33ec66087bc6c5631b85819"

IMAGE_NAME = os.environ.get("IMAGE_NAME")
DEFAULT_PARAMS = dict(
    weka_drivers_handling=True,
    uio_pci_generic=False,
)
version_params = VERSION_TO_DRIVERS_MAP_WEKAFS.get(os.environ.get("IMAGE_NAME").split(":")[-1], DEFAULT_PARAMS)
if "4.2.7.64-s3multitenancy." in IMAGE_NAME:
    version_params = dict(
        wekafs="1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86",
        mpin_user="f8c7f8b24611c2e458103da8de26d545",
        igb_uio="b64e22645db30b31b52f012cc75e9ea0",
        uio_pci_generic="1.0.0-929f279ce026ddd2e31e281b93b38f52",
    )
assert version_params

WEKA_DRIVERS_HANDLING = True if version_params.get("weka_drivers_handling") else False

# Implement the rest of your logic here
import asyncio
import os
import signal

loop = asyncio.get_event_loop()


async def get_weka_version():
    files = os.listdir("/opt/weka/dist/release")
    assert len(files) == 1, Exception(f"More then one release found: {files}")
    version = files[0].partition(".spec")[0]
    return version

async def load_drivers():
    def should_skip_uio_pci_generic():
        return version_params.get('uio_pci_generic') is False or should_skip_uio()

    def should_skip_uio():
        return is_google_cos()

    def should_skip_igb_uio():
        return should_skip_uio()

    if is_rhcos():
        if os.path.isdir("/hostpath/lib/modules"):
            os.system("cp -r /hostpath/lib/modules/* /lib/modules/")

    if not WEKA_DRIVERS_HANDLING:
        # LEGACY MODE
        weka_driver_version = version_params.get('wekafs')
        download_cmds = [
            (f"mkdir -p /opt/weka/dist/drivers", "creating drivers directory"),
            (
                f"curl -kfo /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-$(uname -r).$(uname -m).ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsgw-{weka_driver_version}-$(uname -r).$(uname -m).ko",
                "downloading wekafsgw driver"),
            (
                f"curl -kfo /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-$(uname -r).$(uname -m).ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsio-{weka_driver_version}-$(uname -r).$(uname -m).ko",
                "downloading wekafsio driver"),
            (
                f"curl -kfo /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-$(uname -r).$(uname -m).ko {DIST_SERVICE}/dist/v1/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
                "downloading mpin_user driver")
        ]
        if not should_skip_igb_uio():
            download_cmds.append((
                f"curl -kfo /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-$(uname -r).$(uname -m).ko {DIST_SERVICE}/dist/v1/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
                "downloading igb_uio driver"))
        if not should_skip_uio_pci_generic():
            download_cmds.append((
                f"curl -kfo /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-$(uname -r).$(uname -m).ko {DIST_SERVICE}/dist/v1/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
                "downloading uio_pci_generic driver"))

        load_cmds = [
            (
                f"lsmod | grep -w wekafsgw || insmod /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-$(uname -r).$(uname -m).ko",
                "loading wekafsgw driver"),
            (
                f"lsmod | grep -w wekafsio || insmod /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-$(uname -r).$(uname -m).ko",
                "loading wekafsio driver"),
            (
                f"lsmod | grep -w mpin_user || insmod /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
                "loading mpin_user driver")
        ]
        if not should_skip_uio():
            load_cmds.append((f"lsmod | grep -w uio || modprobe uio", "loading uio driver"))
        if not should_skip_igb_uio():
            load_cmds.append((
                f"lsmod | grep -w igb_uio || insmod /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
                "loading igb_uio driver"))

    else:
        # list directory /opt/weka/dist/version
        # assert single json file and take json filename
        version = await get_weka_version()
        download_cmds = [
            (f"weka driver download --from '{DIST_SERVICE}' --without-agent --version {version}", "Downloading drivers")
        ]
        load_cmds = [
            (f"rmmod wekafsio || echo could not unload old wekafsio driver, still trying to proceed", "unloading wekafsio"),
            (f"rmmod wekafsgw || echo could not unload old wekafsgw driver, still trying to proceed", "unloading wekafsgw"),
            (f"weka driver install --without-agent --version {version}", "loading drivers"),
        ]
    if not should_skip_uio_pci_generic():
        load_cmds.append((
            f"lsmod | grep -w uio_pci_generic || insmod /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-$(uname -r).$(uname -m).ko",
            "loading uio_pci_generic driver"))

    # load vfio-pci if not loaded and iommu groups are present
    cmd = '[ "$(ls -A /sys/kernel/iommu_groups/)" ] && lsmod | grep -w vfio_pci || modprobe vfio-pci'
    _, stderr, ec = await run_command(cmd)
    if ec != 0:
        logging.error(f"Failed to load vfio-pci {stderr.decode('utf-8')}: exc={ec}, last command: {cmd}")
        raise Exception(f"Failed to load vfio-pci: {stderr}")

    logging.info("Downloading and loading drivers")
    for cmd, desc in download_cmds + load_cmds:
        logging.info(f"Driver loading step: {desc}")
        stdout, stderr, ec = await run_command(cmd)
        if ec != 0:
            logging.error(f"Failed to load drivers {stderr.decode('utf-8')}: exc={ec}, last command: {cmd}")
            raise Exception(f"Failed to load drivers: {stderr.decode('utf-8')}")
    logging.info("All drivers loaded successfully")


async def copy_drivers():
    if WEKA_DRIVERS_HANDLING:
        return

    weka_driver_version = version_params.get('wekafs')
    assert weka_driver_version

    stdout, stderr, ec = await run_command(dedent(f"""
      mkdir -p /opt/weka/dist/drivers
      cp /opt/weka/data/weka_driver/{weka_driver_version}/$(uname -r)/wekafsio.ko /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-$(uname -r).$(uname -m).ko
      cp /opt/weka/data/weka_driver/{weka_driver_version}/$(uname -r)/wekafsgw.ko /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-$(uname -r).$(uname -m).ko

      cp /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/$(uname -r)/igb_uio.ko /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-$(uname -r).$(uname -m).ko
      cp /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/$(uname -r)/mpin_user.ko /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-$(uname -r).$(uname -m).ko
      {"" if version_params.get('uio_pci_generic') == False else f"cp /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/$(uname -r)/uio_pci_generic.ko /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-$(uname -r).$(uname -m).ko"}
    """))
    if ec != 0:
        logging.info(f"Failed to copy drivers post build {stderr}: exc={ec}")
        raise Exception(f"Failed to copy drivers post build: {stderr}")
    logging.info("done copying drivers")


async def cos_build_drivers():
    weka_driver_version = version_params["wekafs"]
    weka_driver_file_version = weka_driver_version.rsplit("-", 1)[0]
    mpin_driver_version = version_params["mpin_user"]
    igb_uio_driver_version = version_params["igb_uio"]
    uio_pci_generic_driver_version = version_params.get("uio_pci_generic", "1.0.0-929f279ce026ddd2e31e281b93b38f52")
    weka_driver_squashfs = f'/opt/weka/dist/image/weka-driver-{weka_driver_file_version}.squashfs'
    mpin_driver_squashfs = f'/opt/weka/dist/image/driver-mpin-user-{mpin_driver_version}.squashfs'
    igb_uio_driver_squashfs = f'/opt/weka/dist/image/driver-igb-uio-{igb_uio_driver_version}.squashfs'
    uio_pci_driver_squashfs = f'/opt/weka/dist/image/driver-uio-pci-generic-{uio_pci_generic_driver_version}.squashfs'
    logging.info(f"Building drivers for Google Container-Optimized OS release {OS_BUILD_ID}")
    for cmd, desc in [
        (f"apt-get install -y squashfs-tools", "installing squashfs-tools"),
        (f"mkdir -p /opt/weka/data/weka_driver/{weka_driver_version}/$(uname -r)", "downloading weka driver"),
        (f"mkdir -p /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/$(uname -r)", "downloading mpin driver"),
        (f"mkdir -p /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/$(uname -r)", "downloading igb_uio driver"),
        (f"mkdir -p /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/$(uname -r)",
         "downloading uio_pci_generic driver"),
        (f"unsquashfs -i -f -d /opt/weka/data/weka_driver/{weka_driver_version}/$(uname -r) {weka_driver_squashfs}",
         "extracting weka driver"),
        (f"unsquashfs -i -f -d /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/$(uname -r) {mpin_driver_squashfs}",
         "extracting mpin driver"),
        (f"unsquashfs -i -f -d /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/$(uname -r) {igb_uio_driver_squashfs}",
         "extracting igb_uio driver"),
        (
                f"unsquashfs -i -f -d /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/$(uname -r) {uio_pci_driver_squashfs}",
                "extracting uio_pci_generic driver"),
        (f"cd /opt/weka/data/weka_driver/{weka_driver_version}/$(uname -r) && /devenv.sh -R {OS_BUILD_ID} -m ",
         "building weka driver"),
        (f"cd /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/$(uname -r) && /devenv.sh -R {OS_BUILD_ID} -m",
         "building mpin driver"),
        (f"cd /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/$(uname -r) && /devenv.sh -R {OS_BUILD_ID} -m",
         "building igb_uio driver"),
        (
                f"cd /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/$(uname -r) && /devenv.sh -R {OS_BUILD_ID} -m",
                "building uio_pci_generic driver"),
    ]:
        logging.info(f"COS driver building step: {desc}")
        stdout, stderr, ec = await run_command(cmd)
        if ec != 0:
            logging.error(f"Failed to build drivers {stderr}: exc={ec}, last command: {cmd}")
            raise Exception(f"Failed to build drivers: {stderr}")

    logging.info("Done building drivers")


def parse_cpu_allowed_list(path="/proc/1/status"):
    with open(path) as file:
        for line in file:
            if line.startswith("Cpus_allowed_list"):
                return expand_ranges(line.strip().split(":\t")[1])
    return []


def expand_ranges(ranges_str):
    ranges = []
    for part in ranges_str.split(','):
        if '-' in part:
            start, end = map(int, part.split('-'))
            ranges.extend(list(range(start, end + 1)))
        else:
            ranges.append(int(part))
    return ranges


def read_siblings_list(cpu_index):
    path = f"/sys/devices/system/cpu/cpu{cpu_index}/topology/thread_siblings_list"
    with open(path) as file:
        return expand_ranges(file.read().strip())


@dataclass
class HostInfo:
    kubernetes_distro = 'k8s'
    os = 'unknown'
    os_build_id = ''  # this is either COS build ID OR OpenShift version tag, e.g. 415.92.202406111137-0

    def is_rhcos(self):
        return self.os == OS_NAME_REDHAT_COREOS

    def is_cos(self):
        return self.os == OS_NAME_GOOGLE_COS


def get_host_info():
    raw_data = {}
    ret = HostInfo()
    with open("/hostside/etc/os-release") as file:
        for line in file:
            try:
                k, v = line.strip().split("=")
            except ValueError:
                continue
            if v:
                raw_data[k] = v.strip().replace('"', '')

    ret.os = raw_data.get("ID", "")

    if ret.is_rhcos():
        ret.kubernetes_distro = KUBERNETES_DISTRO_OPENSHIFT
        ret.os_build_id = raw_data.get("VERSION", "")

    elif ret.is_cos():
        ret.kubernetes_distro = KUBERNETES_DISTRO_GKE
        ret.os_build_id = raw_data.get("BUILD_ID", "")
    return ret


@lru_cache
def find_full_cores(n):
    if CORE_IDS != "auto":
        return list(CORE_IDS.split(","))

    selected_siblings = []

    allowed_cores = parse_cpu_allowed_list()

    for cpu_index in allowed_cores:
        if is_rhcos() and cpu_index in [0]:
            continue  # TODO: remove this once a better solution is found to avoid using CPU 0 and 1

        siblings = read_siblings_list(cpu_index)
        if all(sibling in allowed_cores for sibling in siblings):
            if any(sibling in selected_siblings for sibling in siblings):
                continue
            selected_siblings.append(siblings[0])  # Select one sibling (the first for simplicity)
            if len(selected_siblings) == n:
                break

    if len(selected_siblings) < n:
        logging.error(f"Error: cannot find {n} full cores")
        sys.exit(1)
    else:
        return selected_siblings


async def await_agent():
    start = time.time()
    agent_timeout = 60 if WEKA_PERSISTENCE_MODE!="global" else 1500 # global usually is remote storage and pre-create of logs file might take much longer
    while start + agent_timeout > time.time():
        _, _, ec = await run_command("weka local ps")
        if ec == 0:
            logging.info("Weka-agent started successfully")
            return
        await asyncio.sleep(0.3)
        logging.info("Waiting for weka-agent to start")
    raise Exception(f"Agent did not come up in {agent_timeout} seconds")


processes = {}


class Daemon:
    def __init__(self, cmd, alias):
        self.cmd = cmd
        self.alias = alias
        self.process = None
        self.task = None

    async def start(self):
        logging.info(f"Starting daemon {self.alias} with cmd {self.cmd}")
        self.task = asyncio.create_task(self.monitor())
        return self.task

    async def start_process(self):
        logging.info(f"Starting process {self.cmd} for daemon {self.alias}")
        self.process = await start_process(self.cmd, self.alias)
        logging.info(f"Started process {self.cmd} for daemon {self.alias}")

    async def stop(self):
        logging.info(f"Stopping daemon {self.alias}")
        if self.task:
            self.task.cancel()
            try:
                await self.task
            except asyncio.CancelledError:
                pass
        await self.stop_process()

    async def stop_process(self):
        logging.info(f"Stopping process for daemon {self.alias}")
        if self.process:
            await stop_process(self.process)
            self.process = None
            logging.info(f"Stopped process for daemon {self.alias}")
        logging.info(f"No process found to stop")

    async def monitor(self):
        async def with_pause():
            await asyncio.sleep(3)

        while True:
            if self.process:
                if self.is_running():
                    await with_pause()
                    continue
                else:
                    logging.info(f"Daemon {self.alias} is not running")
                    await self.stop_process()
            await self.start_process()

    def is_running(self):
        if self.process is None:
            return False
        running = self.process.returncode is None
        return running


async def start_process(command, alias=""):
    """Start a daemon process."""
    # TODO: Check if already exists, not really needed unless actually adding recovery flow
    # TODO: Logs are basically thrown away into stdout . wrap agent logs as debug on logging level
    process = await asyncio.create_subprocess_shell(command, preexec_fn=os.setpgrp)
    # stdout=asyncio.subprocess.PIPE,
    # stderr=asyncio.subprocess.PIPE)
    logging.info(f"Daemon {alias or command} started with PID {process.pid}")
    processes[alias or command] = process
    logging.info(f"Daemon started with PID {process.pid} for command {command}")
    return process


async def run_command(command, capture_stdout=True, log_execution=True, env: dict = None, log_output=True):
    # TODO: Wrap stdout of commands via INFO via logging
    if log_execution:
        logging.info("Running command: " + command)
    if capture_stdout:
        pipe = asyncio.subprocess.PIPE
    else:
        pipe = None
    process = await asyncio.create_subprocess_shell("set -e\n" + command,
                                                    stdout=pipe,
                                                    stderr=pipe, env=env)
    stdout, stderr = await process.communicate()
    if log_execution:
        logging.info(f"Command {command} finished with code {process.returncode}")
    if stdout and log_output:
        logging.info(f"Command {command} stdout: {stdout.decode('utf-8')}")
    if stderr and log_output:
        logging.info(f"Command {command} stderr: {stderr.decode('utf-8')}")
    return stdout, stderr, process.returncode


async def run_logrotate():
    stdout, stderr, ec = await run_command("logrotate /etc/logrotate.conf", log_execution=False)
    if ec != 0:
        raise Exception(f"Failed to run logrotate: {stderr}")


async def write_logrotate_config():
    with open("/etc/logrotate.conf", "w") as f:
        f.write(dedent("""
            /var/log/syslog /var/log/errors {
                size 1M
                rotate 3
                missingok
                notifempty
                compress
            }
"""))


async def periodic_logrotate():
    while not exiting:
        await write_logrotate_config()
        await run_logrotate()
        await asyncio.sleep(60)


async def resolve_aws_net(device):
    idx = device.split("_")[-1]
    # read configmap
    with open("/etc/wekaio/node-info/node-info.json") as f:
        config = json.load(f)
    nics = config["nics"]
    nics = list(filter(lambda x: not x["primary"], nics))
    nics = list(sorted(nics, key=lambda x: x["mac_address"]))

    nic = nics[int(idx)]
    mac = nic["mac_address"]
    ip = nic["private_ip"]
    mask = "20"  # TODO: Should not be hardcoded! This work in just one env.
    gw = "0.0.0.0"
    # gw = nic["gw"]
    net = f"'{mac}/{ip}/{mask}/{gw}'"
    return net


async def resolve_dhcp_net(device):
    def subnet_mask_to_prefix_length(subnet_mask):
        # Convert subnet mask to binary representation
        binary_mask = ''.join([bin(int(octet) + 256)[3:] for octet in subnet_mask.split('.')])
        # Count the number of 1s in the binary representation
        prefix_length = binary_mask.count('1')
        return prefix_length

    def get_netdev_info(device):
        # Create a socket to communicate with the network interface
        s = None
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)

            # Get the IP address
            ip_address = socket.inet_ntoa(fcntl.ioctl(
                s.fileno(),
                0x8915,  # SIOCGIFADDR
                struct.pack('256s', bytes(device[:15], 'utf-8'))
            )[20:24])

            # Get the netmask
            netmask = socket.inet_ntoa(fcntl.ioctl(
                s.fileno(),
                0x891b,  # SIOCGIFNETMASK
                struct.pack('256s', bytes(device[:15], 'utf-8'))
            )[20:24])
            cidr = subnet_mask_to_prefix_length(netmask)

            # Get the MAC address
            info = fcntl.ioctl(s.fileno(), 0x8927,  # SIOCGIFHWADDR
                               struct.pack('256s', bytes(device[:15], 'utf-8')))
            mac_address = ':'.join('%02x' % b for b in info[18:24])
        finally:
            if s:
                s.close()

        return mac_address, ip_address, cidr

    try:
        mac_address, ip_address, cidr = get_netdev_info(device)
    except OSError:
        raise Exception(f"Failed to get network info for device {device}, no IP address found")

    return f"'{mac_address}/{ip_address}/{cidr}'"


async def create_container():
    full_cores = find_full_cores(NUM_CORES)
    mode_part = ""
    if MODE == "compute":
        mode_part = "--only-compute-cores"
    elif MODE == "drive":
        mode_part = "--only-drives-cores"
    elif MODE == "client":
        mode_part = "--only-frontend-cores"
    elif MODE == "s3":
        mode_part = "--only-frontend-cores"
    elif MODE == "nfs":
        mode_part = "--only-frontend-cores"

    core_str = ",".join(map(str, full_cores))
    logging.info(f"Creating container with cores: {core_str}")

    # read join secret from if file exists /var/run/secrets/weka-operator/operator-user/password
    join_secret_cmd = ""
    join_secret_flag = ""
    if os.path.exists("/var/run/secrets/weka-operator/operator-user/join-secret"):
        join_secret_flag = "--join-secret"
        if MODE == "client":
            join_secret_flag = "--join-token"
        join_secret_cmd = "$(cat /var/run/secrets/weka-operator/operator-user/join-secret)"

    if "aws_" in NETWORK_DEVICE:
        devices = NETWORK_DEVICE.split(",")
        devices = [await resolve_aws_net(dev) for dev in devices]
        net_str = " ".join([f"--net {d}" for d in devices])
    elif is_rhcos():
        # Weka does not know how to resolve the network device when address is assigned via DHCP etc.
        # and we want to limit this behavior only to RHCOS now.
        # TODO: either allow this for all OSes or replace this logic to be inside Weka...
        devices = NETWORK_DEVICE.split(",")
        devices = [await resolve_dhcp_net(dev) for dev in devices]
        net_str = " ".join([f"--net {d}" for d in devices])
    elif ',' in NETWORK_DEVICE:
        net_str = " ".join([f"--net {d}" for d in NETWORK_DEVICE.split(",")])
    else:
        net_str = f"--net {NETWORK_DEVICE}"

    failure_domain = FAILURE_DOMAIN

    command = dedent(f"""
        weka local setup container --name {NAME} --no-start --disable\
        --core-ids {core_str} --cores {NUM_CORES} {mode_part} \
        {net_str}  --base-port {PORT} --memory {MEMORY} \
        {f"{join_secret_flag} {join_secret_cmd}" if join_secret_cmd else ""} \
        {f"--join-ips {JOIN_IPS}" if JOIN_IPS else ""} \
        {f"--client" if MODE == 'client' else ""} \
        {f"--restricted" if MODE == 'client' and "4.2.7.64" not in IMAGE_NAME else ""} \
        {f"--failure-domain {failure_domain}" if failure_domain else ""}
    """)
    logging.info(f"Creating container with command: {command}")
    stdout, stderr, ec = await run_command(command)
    if ec != 0:
        raise Exception(f"Failed to create container: {stderr}")
    logging.info("Container created successfully")


async def configure_traces():
    # {
    #   "enabled": true,
    #   "ensure_free_space_bytes": 3221225472,
    #   "freeze_period": {
    #     "end_time": "0001-01-01T00:00:00+00:00",
    #     "retention": 0,
    #     "start_time": "0001-01-01T00:00:00+00:00"
    #   },
    #   "retention_type": "DEFAULT",
    #   "version": 1
    # }
    data = dict(enabled=True, ensure_free_space_bytes=int(ENSURE_FREE_SPACE_GB) * 1024 * 1024 * 1024,
                retention_bytes=int(MAX_TRACE_CAPACITY_GB) * 1024 * 1024 * 1024, retention_type="BYTES", version=1,
                freeze_period=dict(start_time="0001-01-01T00:00:00+00:00", end_time="0001-01-01T00:00:00+00:00",
                                   retention=0))
    if MODE == 'dist':
        data['enabled'] = False
        data['retention_bytes'] = 128 * 1024 * 1024 * 1024
    data_string = json.dumps(data)

    command = dedent(f"""
        set -e
        mkdir -p /opt/weka/k8s-scripts
        echo '{data_string}' > /opt/weka/k8s-scripts/dumper_config.json.override
        weka local run --container {NAME} mv /opt/weka/k8s-scripts/dumper_config.json.override /data/reserved_space/dumper_config.json.override
        """)
    stdout, stderr, ec = await run_command(command)
    if ec != 0:
        raise Exception(f"Failed to configure traces: {stderr}")
    logging.info("Traces configured successfully")


async def get_containers():
    current_containers, stderr, ec = await run_command("weka local ps --json")
    if ec != 0:
        raise Exception(f"Failed to list containers: {stderr}")
    current_containers = json.loads(current_containers)
    return current_containers


async def get_weka_local_resources() -> dict:
    resources, stderr, ec = await run_command(f"weka local resources --container {NAME} --json", log_output=False)
    if ec != 0:
        raise Exception(f"Failed to get resources: {stderr}")
    return json.loads(resources)


def should_recreate_client_container(resources: dict) -> bool:
    if resources["base_port"] != PORT:
        return True
    if resources.get("restricted_client") is not True:
        return True
    return False


def convert_to_bytes(memory: str) -> int:
    size_str = memory.upper()
    match = re.match(r"(\d+)([KMGTPE]I?B)", size_str)
    if not match:
        raise ValueError(f"Invalid size format: {size_str}")

    size = int(match.group(1))
    unit = match.group(2)

    multipliers = {
        'B': 1,
        'KB': 10**3,
        'MB': 10**6,
        'GB': 10**9,
        'TB': 10**12,
        'PB': 10**15,
        'EB': 10**18,
        'KIB': 2**10,
        'MIB': 2**20,
        'GIB': 2**30,
        'TIB': 2**40,
        'PIB': 2**50,
        'EIB': 2**60
    }
    return size * multipliers[unit]


async def ensure_weka_container():
    current_containers = await get_containers()

    if len(current_containers) == 0:
        logging.info("no pre-existing containers, creating")
        # create container
        if MODE in ["compute", "drive", "client", "s3", "nfs"]:
            await create_container()
        else:
            raise NotImplementedError(f"Unsupported mode: {MODE}")

    full_cores = find_full_cores(NUM_CORES)

    # reconfigure containers
    logging.info("Container already exists, reconfiguring")
    resources = await get_weka_local_resources()

    if MODE == "client" and should_recreate_client_container(resources):
        logging.info("Recreating client container")
        await run_command("weka local stop --force", capture_stdout=False)
        await run_command(f"weka local rm --all --force", capture_stdout=False)
        await create_container()
        resources = await get_weka_local_resources()


    #TODO: Normalize to have common logic between setup and reconfigure, including between clients and backends
    if MODE == "client" and len(resources['nodes']) != (NUM_CORES+1):
        stdout, stderr, ec = await run_command(f"weka local resources cores -C {NAME} --only-frontend-cores {NUM_CORES} --core-ids {','.join(map(str, full_cores[:NUM_CORES]))}")
        if ec != 0:
            raise Exception(f"Failed to get frontend cores: {stderr}")

    memory_bytes = convert_to_bytes(MEMORY)
    if resources['memory'] != memory_bytes:
        logging.info(f"Reconfiguring memory from {resources['memory']} to {memory_bytes} ({MEMORY})")
        stdout, stderr, ec = await run_command(f"weka local resources memory -C {NAME} {MEMORY}")
        if ec != 0:
            raise Exception(f"Failed to reconfigure memory: {stderr}")

    # TODO: unite with above block as single getter
    resources = await get_weka_local_resources()

    if MODE in ["s3", "nfs"]:
        resources['allow_protocols'] = True
    resources['reserve_1g_hugepages'] = False
    resources['excluded_drivers'] = ["igb_uio"]
    resources['auto_discovery_enabled'] = False

    resources["mask_interrupts"] = True

    cores_cursor = 0
    for node_id, node in resources['nodes'].items():
        if node['dedicate_core']:
            node['core_id'] = full_cores[cores_cursor]
            cores_cursor += 1

    # fix/add gateway
    if NET_GATEWAY:
        if NETWORK_DEVICE not in ['udp', ""]:
            #TODO: Multi-nic support with custom gateways
            if len(resources['net_devices']) != 1:
                logging.error("Gateway configuration is not supported with multiple or zero NICs")
            resources['net_devices'][0]['gateway'] = NET_GATEWAY

    # save resources
    with open("/tmp/weka-resources.json", "w") as f:
        json.dump(resources, f)
    # reconfigure containers
    stdout, stderr, ec = await run_command(f"""
        mv /tmp/weka-resources.json /opt/weka/data/{NAME}/container/resources-operator.json
        ln -sf resources-operator.json /opt/weka/data/{NAME}/container/resources.json
        ln -sf resources-operator.json /opt/weka/data/{NAME}/container/resources.json.stable
        ln -sf resources-operator.json /opt/weka/data/{NAME}/container/resources.json.staging
    """)

    if ec != 0:
        raise Exception(f"Failed to import resources: {stderr} \n {stdout}")


async def start_weka_container():
    stdout, stderr, ec = await run_command("weka local start")
    if ec != 0:
        raise Exception(f"Failed to start container: {stderr}")
    logging.info("finished applying new config")
    logging.info(f"Container reconfigured successfully: {stdout.decode('utf-8')}")


async def configure_persistency():
    if not WEKA_PERSISTENCE_DIR:
        return

    command = dedent(f"""
        mkdir -p /opt/weka-preinstalled
        # --- save weka image data separately
        mount -o bind /opt/weka /opt/weka-preinstalled
        # --- WEKA_PERSISTENCE_DIR - is HostPath (persistent volume)
        # --- put existing drivers from persistent dir to weka-preinstalled
        mkdir -p {WEKA_PERSISTENCE_DIR}/dist/drivers
        mount -o bind {WEKA_PERSISTENCE_DIR}/dist/drivers /opt/weka-preinstalled/dist/drivers
        mount -o bind {WEKA_PERSISTENCE_DIR} /opt/weka
        mkdir -p /opt/weka/dist
        # --- put weka dist back on top
        mount -o bind /opt/weka-preinstalled/dist /opt/weka/dist
        # --- make drivers dir persistent
        mount -o bind {WEKA_PERSISTENCE_DIR}/dist/drivers /opt/weka/dist/drivers

        if [ -d /opt/k8s-weka/boot-level ]; then
            BOOT_DIR=/opt/k8s-weka/boot-level/$(cat /proc/sys/kernel/random/boot_id)
            mkdir -p $BOOT_DIR
            mkdir -p /opt/weka/external-mounts/cleanup
            mount -o bind $BOOT_DIR /opt/weka/external-mounts/cleanup
        fi
        
        if [ -f /var/run/secrets/weka-operator/wekahome-cacert/cert.pem ]; then
            rm -rf /opt/weka/k8s-runtime/vars/wh-cacert
            mkdir -p /opt/weka/k8s-runtime/vars/wh-cacert/
            cp /var/run/secrets/weka-operator/wekahome-cacert/cert.pem /opt/weka/k8s-runtime/vars/wh-cacert/cert.pem
            chmod 400 /opt/weka/k8s-runtime/vars/wh-cacert/cert.pem
        fi
        
        if [ -d /opt/k8s-weka/node-cluster ]; then
            ENVOY_DIR=/opt/weka/envoy
            EXT_ENVOY_DIR=/opt/k8s-weka/node-cluster/envoy
            mkdir -p $ENVOY_DIR
            mkdir -p $EXT_ENVOY_DIR
            mount -o bind $EXT_ENVOY_DIR $ENVOY_DIR
        fi
        
        mkdir -p {WEKA_K8S_RUNTIME_DIR}
        touch {PERSISTENCY_CONFIGURED}
    """)

    stdout, stderr, ec = await run_command(command)
    if ec != 0:
        raise Exception(f"Failed to configure persistency: {stderr}")

    logging.info("Persistency configured successfully")


async def ensure_weka_version():
    cmd = "weka version | grep '*' || weka version set $(weka version)"
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to set weka version: {stderr}")
    logging.info("Weka version set successfully")


async def configure_agent(agent_handle_drivers=False):
    logging.info(f"reconfiguring agent with handle_drivers={agent_handle_drivers}")
    ignore_driver_flag = "false" if agent_handle_drivers else "true"

    env_vars = dict()

    skip_envoy_setup = ""
    if MODE == "s3":
        skip_envoy_setup = "sed -i 's/skip_envoy_setup=.*/skip_envoy_setup=true/g' /etc/wekaio/service.conf || true"

    if MODE == "envoy":
        env_vars['RESTART_EPOCH_WANTED'] = str(int(os.environ.get("envoy_restart_epoch", time.time())))
        env_vars['BASE_ID'] = PORT

    expand_condition_mounts = ""
    if MODE in ['envoy', 's3']:
        expand_condition_mounts = ",envoy-data"

    drivers_handling_cmd = f"""
    # Check if the last line contains the pattern
    CONFFILE="/etc/wekaio/service.conf"
    PATTERN="skip_driver_install"
    if tail -n 1 "$CONFFILE" | grep -q "$PATTERN"; then
        sed -i '$d' "$CONFFILE"
    fi


    #TODO: once moving to 4.3+ only switch to ignore_driver_spec. Problem that 4.2 had it in different category
    # and check by skip_driver_install is sort of abuse of not anymore existing flag to have something to validate by
    if ! grep -q "skip_driver_install" /etc/wekaio/service.conf; then
        sed -i "/\[os\]/a skip_driver_install={ignore_driver_flag}" /etc/wekaio/service.conf
        sed -i "/\[os\]/a ignore_driver_spec={ignore_driver_flag}" /etc/wekaio/service.conf
    else
        sed -i "s/skip_driver_install=.*/skip_driver_install={ignore_driver_flag}/g" /etc/wekaio/service.conf
    fi
    sed -i "s/ignore_driver_spec=.*/ignore_driver_spec={ignore_driver_flag}/g" /etc/wekaio/service.conf || true

    sed -i "s@external_mounts=.*@external_mounts=/opt/weka/external-mounts@g" /etc/wekaio/service.conf || true
    sed -i "s@conditional_mounts_ids=.*@conditional_mounts_ids=etc-hosts,etc-resolv{expand_condition_mounts}@g" /etc/wekaio/service.conf || true
    {skip_envoy_setup}
    """

    cmd = dedent(f"""
        {drivers_handling_cmd}
        sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true
        sed -i "s/port=14100/port={AGENT_PORT}/g" /etc/wekaio/service.conf || true
        # sed -i "s/serve_static=false/serve_static=true/g" /etc/wekaio/service.conf || true
        echo '{{"agent": {{"port": \'{AGENT_PORT}\'}}}}' > /etc/wekaio/service.json
    """)
    stdout, stderr, ec = await run_command(cmd, env=env_vars)
    if ec != 0:
        raise Exception(f"Failed to configure agent: {stderr}")

    if MACHINE_IDENTIFIER is not None:
        os.makedirs("/opt/weka/data/agent", exist_ok=True)
        cmd = f"echo '{MACHINE_IDENTIFIER}' > /opt/weka/data/agent/machine-identifier"
        stdout, stderr, ec = await run_command(cmd)
        if ec != 0:
            raise Exception(f"Failed to set machine-id: {stderr}")
    logging.info("Agent configured successfully")



async def override_dependencies_flag():
    """Hard-code the success marker so that the dist container can start

    Equivalent to:
        ```sh
        HARDCODED=1.0.0-024f0fdaa33ec66087bc6c5631b85819
        mkdir -p /opt/weka/data/dependencies/HARDCODED/$(uname -r)/
        touch /opt/weka/data/dependencies/HARDCODED/$(uname -r)/successful
        ```
    """
    logging.info("overriding dependencies flag")
    dep_version = version_params.get('dependencies', DEFAULT_DEPENDENCY_VERSION)

    if WEKA_DRIVERS_HANDLING:
        cmd = dedent("""
        mkdir -p /opt/weka/data/dependencies
        touch /opt/weka/data/dependencies/skip
        """)
    else:
        cmd = dedent(
            f"""
            mkdir -p /opt/weka/data/dependencies/{dep_version}/$(uname -r)/
            touch /opt/weka/data/dependencies/{dep_version}/$(uname -r)/successful
            """
        )
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to override dependencies flag: {stderr}")
    logging.info("dependencies flag overridden successfully")


async def ensure_stem_container(name="dist"):
    logging.info("ensuring dist container")

    cmd = dedent(f"""
        if [ -d /driver-toolkit-shared ]; then
            # Mounting kernel modules from driver-toolkit-shared to dist container
            mkdir -p /lib/modules
            mkdir -p /usr/src
            mount -o bind /driver-toolkit-shared/lib/modules /lib/modules
            mount -o bind /driver-toolkit-shared/usr/src /usr/src
        fi

        weka local ps | grep {name} || weka local setup container --name {name} --net udp --base-port {PORT} --no-start --disable
        """)
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to create dist container: {stderr}")

    logging.info("dist container created successfully")
    # wait for container to become running


async def start_stem_container():
    logging.info("starting dist container")
    # stdout, stderr, ec = await run_command(cmd)
    # if ec != 0:
    #     raise Exception(f"Failed to start dist container: {stderr}")
    # ! start_process is deprecated and this is the only place that uses it
    # TODO: Revalidate if it needed or can be simple run_command(As it should be)
    # TODO: Still broken! hangs if running "weka local start" directly via run_command. zombie process
    await start_process(
        "weka local start")  # weka local start is not returning, so we need to daemonize it, this is a hack that needs to go away
    # reason of being stuck: agent tries to authenticate using admin:admin into this stem container, for not known reason
    logging.info("stem container started")


async def ensure_container_exec():
    logging.info("ensuring container exec")
    start = time.time()
    while True:
        stdout, stderr, ec = await run_command(f"weka local exec --container {NAME} -- ls")
        if ec == 0:
            break
        await asyncio.sleep(1)
        if time.time() - start > 300:
            raise Exception(f"Failed to exec into container in 5 minutes: {stderr}")
    logging.info("container exec ensured")


def write_results(results):
    logging.info("Writing result into /weka-runtime/results.json, results: \n%s", results)
    os.makedirs("/weka-runtime", exist_ok=True)
    with open("/weka-runtime/results.json.tmp", "w") as f:
        json.dump(results, f)
    os.rename("/weka-runtime/results.json.tmp", "/weka-runtime/results.json")


async def discovery():
    # TODO: We should move here everything else we need to discover per node
    # This might be a good place to discover drives as well, as long we have some selector to discover by
    host_info = get_host_info()
    data = dict(
        is_ht=len(read_siblings_list(0)) > 1,
        kubernetes_distro=host_info.kubernetes_distro,
        os=host_info.os,
        os_build_id=host_info.os_build_id,
        schema=DISCOVERY_SCHEMA,
    )
    write_results(data)


async def install_gsutil():
    logging.info("Installing gsutil")
    await run_command("curl https://sdk.cloud.google.com | bash -s -- --disable-prompts")
    os.environ["PATH"] += ":/root/google-cloud-sdk/bin"
    await run_command("gcloud auth activate-service-account --key-file=$GOOGLE_APPLICATION_CREDENTIALS")


async def cleanup_traces_and_stop_dumper():
    while True:
        cmd = "weka local exec supervisorctl status | grep RUNNING"
        stdout, stderr, ec = await run_command(cmd)
        if ec != 0:
            logging.info(f"Failed to get supervisorctl status: {stderr}")
            await asyncio.sleep(3)
            continue
        break

    stdout, stderr, ec = await run_command("""
    weka local exec supervisorctl stop weka-trace-dumper
    rm -f /opt/weka/traces/*.shard
    """)
    if ec != 0:
        logging.error(f"Failed to cleanup traces: {stderr}")


def get_agent_cmd():
    return f"exec /usr/bin/weka --agent --socket-name weka_agent_ud_socket_{AGENT_PORT}"


daemons = {

}


# k8s lifecycle/local leadership election


def cos_reboot_machine():
    logging.warning("Rebooting the host")
    os.sync()
    time.sleep(3)  # give some time to log the message and sync
    os.system("echo b > /hostside/proc/sysrq-trigger")


async def is_secure_boot_enabled():
    stdout, stderr, ec = await run_command("dmesg")
    return "Secure boot enabled" in stdout.decode('utf-8')


async def cos_disable_driver_signing_verification():
    logging.info("Checking if driver signing is disabled")
    esp_partition = "/dev/disk/by-partlabel/EFI-SYSTEM"
    mount_path = "/tmp/esp"
    grub_cfg = "efi/boot/grub.cfg"
    sed_cmds = []
    reboot_required = False

    with open("/hostside/proc/cmdline", 'r') as file:
        for line in file.readlines():
            logging.info(f"cmdline: {line}")
            if "module.sig_enforce" in line:
                if "module.sig_enforce=1" in line:
                    sed_cmds.append(('module.sig_enforce=1', 'module.sig_enforce=0'))
            else:
                sed_cmds.append(('cros_efi', 'cros_efi module.sig_enforce=0'))
            if "loadpin.enabled" in line:
                if "loadpin.enabled=1" in line:
                    sed_cmds.append(('loadpin.enabled=1', 'loadpin.enabled=0'))
            else:
                sed_cmds.append(('cros_efi', 'cros_efi loadpin.enabled=0'))
            if "loadpin.enforce" in line:
                if "loadpin.enforce=1" in line:
                    sed_cmds.append(('loadpin.enforce=1', 'loadpin.enforce=0'))
            else:
                sed_cmds.append(('cros_efi', 'cros_efi loadpin.enforce=0'))
    if sed_cmds:
        logging.warning("Must modify kernel parameters")
        if WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING:
            logging.warning("Node driver signing configuration has changed, NODE WILL REBOOT NOW!")
        else:
            raise Exception(
                "Node driver signing configuration must be changed, but WEKA_COS_ALLOW_DISABLE_DRIVER_SIGNING is not set to True. Exiting.")

        await run_command(f"mkdir -p {mount_path}")
        await run_command(f"mount {esp_partition} {mount_path}")
        current_path = os.curdir
        try:
            os.chdir(mount_path)
            for sed_cmd in sed_cmds:
                await run_command(f"sed -i 's/{sed_cmd[0]}/{sed_cmd[1]}/g' {grub_cfg}")
            reboot_required = True
        except Exception as e:
            logging.error(f"Failed to modify kernel cmdline: {e}")
            raise
        finally:
            os.chdir(current_path)
            await run_command(f"umount {mount_path}")
            if reboot_required:
                cos_reboot_machine()
    else:
        logging.info("Driver signing is already disabled")


async def cos_configure_hugepages():
    if not is_google_cos():
        logging.debug("Skipping hugepages configuration")
        return

    logging.info("Checking if hugepages are set")
    esp_partition = "/dev/disk/by-partlabel/EFI-SYSTEM"
    mount_path = "/tmp/esp"
    grub_cfg = "efi/boot/grub.cfg"
    sed_cmds = []
    reboot_required = False

    current_path = os.curdir
    with open("/hostside/proc/cmdline", 'r') as file:
        for line in file.readlines():
            logging.info(f"cmdline: {line}")
            if "hugepagesz=" in line:
                if "hugepagesz=1g" in line.lower() and WEKA_COS_GLOBAL_HUGEPAGE_SIZE == "2m":
                    sed_cmds.append(('hugepagesz=1g', 'hugepagesz=2m'))
                elif "hugepagesz=2m" in line.lower() and WEKA_COS_GLOBAL_HUGEPAGE_SIZE == "1g":
                    sed_cmds.append(('hugepagesz=2m', 'hugepagesz=1g'))
            if "hugepages=" not in line:
                # hugepages= is not set at all
                sed_cmds.append(('cros_efi', f'cros_efi hugepages={WEKA_COS_GLOBAL_HUGEPAGE_COUNT}'))
            elif f"hugepages={WEKA_COS_GLOBAL_HUGEPAGE_COUNT}" not in line and WEKA_COS_ALLOW_HUGEPAGE_CONFIG:
                # hugepages= is set but not to the desired value, and we are allowed to change it
                sed_cmds.append(('hugepages=[0-9]+', f'hugepages={WEKA_COS_GLOBAL_HUGEPAGE_COUNT}'))
            elif f"hugepages={WEKA_COS_GLOBAL_HUGEPAGE_COUNT}" not in line and not WEKA_COS_ALLOW_HUGEPAGE_CONFIG:
                logging.info(f"Node hugepages configuration is managed externally, skipping")
    if sed_cmds:
        logging.warning("Must modify kernel HUGEPAGES parameters")
        if WEKA_COS_ALLOW_HUGEPAGE_CONFIG:
            logging.warning("Node hugepage configuration has changed, NODE WILL REBOOT NOW!")
        else:
            raise Exception(
                "Node hugepage configuration must be changed, but WEKA_COS_ALLOW_HUGEPAGE_CONFIG is not set to True. Exiting.")

        await run_command(f"mkdir -p {mount_path}")
        await run_command(f"mount {esp_partition} {mount_path}")
        try:
            os.chdir(mount_path)
            for sed_cmd in sed_cmds:
                await run_command(f"sed -i 's/{sed_cmd[0]}/{sed_cmd[1]}/g' {grub_cfg}")
            reboot_required = True
        except Exception as e:
            logging.error(f"Failed to modify kernel cmdline: {e}")
            raise
        finally:
            os.chdir(current_path)
            os.sync()
            await run_command(f"umount {mount_path}")
            if reboot_required:
                cos_reboot_machine()
    else:
        logging.info(f"Hugepages are already configured to {WEKA_COS_GLOBAL_HUGEPAGE_COUNT}x2m pages")


async def disable_driver_signing():
    if not is_google_cos():
        return
    logging.info("Ensuring driver signing is disabled")
    await cos_disable_driver_signing_verification()


SOCKET_NAME = '\0weka_runtime_' + NAME  # Abstract namespace socket
WEKA_K8S_RUNTIME_DIR = '/opt/weka/k8s-runtime'
GENERATION_PATH = f'{WEKA_K8S_RUNTIME_DIR}/runtime-generation'
CURRENT_GENERATION = str(time.time())
PERSISTENCY_CONFIGURED = f'{WEKA_K8S_RUNTIME_DIR}/persistency-configured'


async def write_generation():
    while WEKA_PERSISTENCE_DIR and not os.path.exists(PERSISTENCY_CONFIGURED):
        logging.info("Waiting for persistency to be configured")
        await asyncio.sleep(1)

    logging.info("Writing generation %s", CURRENT_GENERATION)
    os.makedirs(WEKA_K8S_RUNTIME_DIR, exist_ok=True)
    with open(GENERATION_PATH, 'w') as f:
        f.write(CURRENT_GENERATION)
    logging.info("current generation: %s", read_generation())


def read_generation():
    try:
        with open(GENERATION_PATH, 'r') as f:
            ret = f.read().strip()
    except Exception as e:
        logging.error("Failed to read generation: %s", e)
        ret = ""
    return ret


async def obtain_lock():
    server = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
    server.setblocking(False)
    server.bind(SOCKET_NAME)
    return server


_server = None


async def ensure_envoy_container():
    logging.info("ensuring envoy container")
    cmd = dedent(f"""
        weka local ps | grep envoy || weka local setup envoy
    """)
    _, _, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to ensure envoy container")
    pass


def write_file(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, 'w') as f:
        f.write(content)


async def is_port_free(port: int) -> bool:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        try:
            s.bind(('localhost', port))
            return True
        except OSError as e:
            if e.errno == 98: # Address already in use
                logging.debug(f"Port {port} is already in use")
                return False

            logging.error(f"Failed to bind to port {port}: {e}")
            return False


async def get_free_subrange_in_port_range(
    base_port: int,
    max_port: int,
    subrange_size: int,
    exclude_ports: Optional[List[int]] = None
) -> Tuple[int, int]:
    """Get a subrange of free ports of size subrange_size in the specified port range."""
    exclude_ports = sorted(exclude_ports or [])
    free_ports = set()
    not_free_ports = set()

    port = base_port
    while port <= max_port - subrange_size:
        # Skip any subranges that intersect with exclude_ports
        for exclude_port in exclude_ports:
            if port <= exclude_port < port + subrange_size:
                port = exclude_port + 1
                break
        else:
            subrange_start = port
            consecutive_free_count = 0

            for check_port in range(port, port + subrange_size):
                if check_port in free_ports:
                    consecutive_free_count += 1
                elif check_port in not_free_ports:
                    break
                else:
                    if await is_port_free(check_port):
                        free_ports.add(check_port)
                        consecutive_free_count += 1
                    else:
                        not_free_ports.add(check_port)
                        break

            if consecutive_free_count == subrange_size:
                logging.info(f"Found free subrange: {subrange_start}-{subrange_start + subrange_size - 1}")
                return subrange_start, subrange_start + subrange_size - 1

            # If not all ports in the subrange were free, move to the next port
            port += 1

    raise RuntimeError(f"Could not find a subrange of {subrange_size} free ports in the specified range.")


async def get_free_port(base_port: int, max_port: int, exclude_ports: Optional[List[int]] = None) -> int:
    for port in range(base_port, max_port):
        if exclude_ports and port in exclude_ports:
            continue

        if await is_port_free(port):
            logging.info(f"Found free port: {port}")
            return port

    raise RuntimeError(f"Failed to find free port in range {base_port}-{max_port}")


async def ensure_client_ports():
    global PORT, AGENT_PORT
    logging.info("Ensuring client ports")

    if parse_port(PORT) > 0 and parse_port(AGENT_PORT) > 0:  # we got resources via env, so no need to wait here
        await save_weka_ports_data()
        return

    base_port = parse_port(BASE_PORT)
    port_range = parse_port(PORT_RANGE)
    assert base_port > 0, "BASE_PORT is not set"
    max_port = base_port + port_range if port_range > 0 else MAX_PORT

    try:
        if not parse_port(AGENT_PORT):
            p = await get_free_port(base_port, max_port)
            AGENT_PORT = f'{p}'
        if not parse_port(PORT):
            p1, _ = await get_free_subrange_in_port_range(base_port, max_port, WEKA_CONTAINER_PORT_SUBRANGE, exclude_ports=[int(AGENT_PORT)])
            PORT = f'{p1}'
    except RuntimeError as e:
        raise Exception(f"Failed to find free ports: {e}")
    else:
        await save_weka_ports_data()

async def save_weka_ports_data():
    write_file("/opt/weka/k8s-runtime/vars/port", str(PORT))
    write_file("/opt/weka/k8s-runtime/vars/agent_port", str(AGENT_PORT))
    logging.info(f"PORT={PORT}, AGENT_PORT={AGENT_PORT}")


def parse_port(port_str: str) -> int:
        try:
            return int(port_str)
        except ValueError:
            return 0


async def get_requested_drives():
    if not os.path.exists("/opt/weka/k8s-runtime/resources.json"):
        return []
    with open("/opt/weka/k8s-runtime/resources.json", "r") as f:
        data = json.load(f)
    return data.get("drives", [])


async def wait_for_resources():
    global PORT, AGENT_PORT, RESOURCES, FAILURE_DOMAIN

    if MODE == 'client':
        await ensure_client_ports()

    if MODE not in ['drive', 's3', 'compute', 'nfs', 'envoy']:
        return

    logging.info("waiting for controller to set resources")

    while not os.path.exists("/opt/weka/k8s-runtime/resources.json"):
        logging.info("waiting for /opt/weka/k8s-runtime/resources.json")
        await asyncio.sleep(3)
        continue

    with open("/opt/weka/k8s-runtime/resources.json", "r") as f:
        data = json.load(f)


    RESOURCES = data
    if FAILURE_DOMAIN_LABEL:
        FAILURE_DOMAIN = data.get("failureDomain")
        logging.info("Failure Domain: %s", FAILURE_DOMAIN)
    if parse_port(PORT) == 0 and MODE != 'envoy':
        PORT = data["wekaPort"]
    if parse_port(AGENT_PORT) == 0:
        AGENT_PORT = data["agentPort"]

    await save_weka_ports_data()


async def ensure_drives():
    sys_drives = await find_weka_drives()
    requested_drives = RESOURCES.get("drives", [])
    drives_to_setup = []
    for drive in requested_drives:
        for sd in sys_drives:
            if sd["serial_id"] == drive:
                drives_to_setup.append(sd["block_device"])
                break
        # else:
        #     raise Exception(f"Drive {drive['serial_id']} not found")

    # write discovered drives into runtime dir
    os.makedirs("/opt/weka/k8s-runtime", exist_ok=True)
    with open("/opt/weka/k8s-runtime/drives.json", "w") as f:
        json.dump([d for d in sys_drives if d['serial_id'] in requested_drives], f)
    logging.info(f"sys_drives: {sys_drives}")
    logging.info(f"requested_drives: {requested_drives}")
    logging.info(f"in-kernel drives are: {drives_to_setup}")


is_legacy_driver_command = None


async def is_legacy_driver_cmd() -> bool:
    global is_legacy_driver_command
    if is_legacy_driver_command is not None:
        return is_legacy_driver_command
    cmd = "weka driver --help | grep pack"
    stdout, stderr, ec = await run_command(cmd)
    if ec == 0:
        logging.info("Driver pack command is available, new dist mode")
        is_legacy_driver_command = False
        return False
    logging.info("Driver pack command is not available, legacy dist mode")
    is_legacy_driver_command = True
    return True


async def pack_drivers():
    logging.info("Packing drivers")
    cmd = "weka driver pack"
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to pack drivers: {stderr}")
    logging.info("Drivers packed successfully")


async def main():
    host_info = get_host_info()
    global OS_DISTRO, OS_BUILD_ID
    OS_DISTRO = host_info.os
    logging.info(f'OS_DISTRO={OS_DISTRO}')

    OS_BUILD_ID = host_info.os_build_id

    if not OS_BUILD_ID and is_google_cos():
        raise Exception("OS_BUILD_ID is not set")
    if is_google_cos():
        logging.info(f'OS_BUILD_ID={OS_BUILD_ID}')

    DIST_LEGACY_MODE = await is_legacy_driver_cmd()
    if MODE == "drivers-loader":
        # self signal to exit
        await override_dependencies_flag()
        # 2 minutes timeout for driver loading
        end_time = time.time() + 120
        await disable_driver_signing()
        loaded = False
        while time.time() < end_time:
            try:
                await load_drivers()
                write_results(dict(
                    err=None,
                    drivers_loaded=True
                ))
                logging.info("Drivers loaded successfully")
                loaded = True
                return
            except Exception as e:
                await asyncio.sleep(1)
                if time.time() > end_time:
                    write_results(dict(
                        err=getattr(e, 'message', repr(e)),
                        drivers_loaded=False,
                    ))
                    raise e
                logging.info("retrying drivers download... will reach timeout in %d seconds", end_time - time.time())
        if not loaded:
            raise Exception("Failed to load drivers")
        return

    if MODE == "discovery":
        # self signal to exit
        logging.info("discovery mode")
        await cos_configure_hugepages()
        await discovery()
        return

    await configure_persistency()
    await wait_for_resources()
    await write_generation()  # write own generation to kill other processes
    global _server
    _server = await obtain_lock()  # then waiting for lock with short timeout

    await configure_agent()
    syslog = Daemon("/usr/sbin/syslog-ng -F -f /etc/syslog-ng/syslog-ng.conf", "syslog")
    await syslog.start()

    await override_dependencies_flag()
    if MODE not in ["dist", "drivers-dist", "drivers-loader", "drivers-builder", "adhoc-op-with-container", "envoy", "adhoc-op"]:
        await ensure_drivers()

    agent_cmd = get_agent_cmd()
    agent = Daemon(agent_cmd, "agent")
    await agent.start()
    await await_agent()

    await ensure_weka_version()

    if MODE == "drivers-dist":
        # Dist is only serving, we will invoke downloads on it, probably in stand-alone ad-hoc container, but never actually build
        # if DIST_LEGACY_MODE:
        logging.info("dist-service flow")
        await ensure_stem_container("dist")
        await configure_traces()
        await start_stem_container()
        await cleanup_traces_and_stop_dumper()
        return

    if MODE == "adhoc-op-with-container":
        global NAME
        NAME = "adhoc"
        await ensure_stem_container(NAME)
        await configure_traces()
        await start_stem_container()
        await ensure_container_exec()
        instruction = json.loads(INSTRUCTIONS)
        if instruction.get('force-resign-drives'):
            device_paths = instruction['force-resign-drives'].get('device_paths', [])
            device_serials = instruction['force-resign-drives'].get('device_serials', [])
            if device_paths:
                await force_resign_drives_by_paths(device_paths)
            elif device_serials:
                await force_resign_drives_by_serials(device_serials)
        elif instruction.get('type'):
            signed_drives = await sign_drives(instruction)
            logging.info(f"signed_drives: {signed_drives}")
            await discover_drives()
        else:
            raise ValueError(f"Unsupported instruction: {INSTRUCTIONS}")
        return

    if MODE == "adhoc-op":
        if INSTRUCTIONS == "discover-drives":
            await discover_drives()
        else:
            raise ValueError(f"Unsupported instruction: {INSTRUCTIONS}")
        return

    # de-facto, both drivers-builder and dist right now are doing "build and serve"
    if MODE in ["dist", "drivers-builder"]:
        logging.info("dist-service flow")
        if is_google_cos():
            await install_gsutil()
            await cos_build_drivers()

        elif DIST_LEGACY_MODE:  # default
            await agent.stop()
            await configure_agent(agent_handle_drivers=True)
            await agent.start()  # here the build happens
            await await_agent()

        await ensure_stem_container("dist")
        await configure_traces()
        if not DIST_LEGACY_MODE:
            await pack_drivers()  # explicit pack of drivers if supported, which is new method, that should become default with rest of code removed eventually
        else:
            await agent.stop()
            await configure_agent(agent_handle_drivers=False)
            await agent.start()
            await await_agent()

        if DIST_LEGACY_MODE:
            await copy_drivers()
        await start_stem_container()
        await cleanup_traces_and_stop_dumper()
        weka_version, _, _ = await run_command("weka version current")
        write_results(
            {
                "driver_built": True,
                "err": "",
                "weka_version": weka_version.decode().strip(),
                "kernel_signature": await get_kernel_signature(weka_pack_supported=not DIST_LEGACY_MODE, weka_drivers_handling=WEKA_DRIVERS_HANDLING),
                "weka_pack_not_supported": DIST_LEGACY_MODE,
                "no_weka_drivers_handling": not WEKA_DRIVERS_HANDLING,
            })
        return

    if MODE == "envoy":
        await ensure_envoy_container()
        return

    await ensure_weka_container()
    await configure_traces()
    await start_weka_container()
    await ensure_container_exec()
    logging.info("Container is UP and running")
    if MODE == "drive":
        await ensure_drives()


async def get_kernel_signature(weka_pack_supported=False, weka_drivers_handling=False):
    if not weka_drivers_handling:
        return ""

    cmd = ""
    if weka_pack_supported:
        cmd = "weka driver kernel 2>&1 | awk '{printf \"%s\", $NF}'"
    else:
        # tr -d '\0' is needed to remove null character from the end of output
        cmd = "weka driver kernel-sig 2>&1 | awk '{printf \"%s\", $NF}' | tr -d '\\0'"

    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to get kernel signature: {stderr}")

    res = stdout.decode().strip()
    assert res, "Kernel signature not found"
    return res


async def stop_process(process):
    logging.info(f"stopping daemon with pid {process.pid} (via process group), {process}")

    async def cleanup_process():
        for k, v in list(processes.items()):
            if v == process:
                logging.info(f"removing process {k}")
                del processes[k]
        logging.info(f"waiting for process {process.pid} to exit")
        await process.wait()
        logging.info(f"process {process.pid} exited")

    if process.returncode is not None:
        await cleanup_process()
        return

    pgid = os.getpgid(process.pid)
    logging.info(f"stopping process group {pgid}")
    os.killpg(pgid, signal.SIGTERM)
    logging.info(f"process group {pgid} stopped")
    await cleanup_process()


def is_wrong_generation():
    if MODE in ['drivers-loader', 'discovery']:
        return False

    current_generation = read_generation()
    if current_generation == "":
        return False

    if current_generation != CURRENT_GENERATION:
        logging.error("Wrong generation detected, exiting, current:%s, read: %s", CURRENT_GENERATION, read_generation())
        return True
    return False


async def takeover_shutdown():
    while not is_wrong_generation():
        await asyncio.sleep(1)

    await run_command("weka local stop --force", capture_stdout=False)



def get_active_mounts(file_path="/proc/wekafs/interface") -> int:
    """Get the number of active mounts from the specified file.
    Return -1 if the number of active mounts cannot be determined.
    """
    try:
        with open(file_path, "r") as file:
            for line in file:
                if line.startswith("Active mounts:"):
                    # Extract the number after "Active mounts:"
                    return int(line.split(":")[1].strip())
    except FileNotFoundError:
        logging.error(f"File '{file_path}' not found.")
    except ValueError:
        logging.error(f"Failed to parse the number of active mounts.")
    except Exception as e:
        logging.error(f"Failed to get the number of active mounts: {e}")
    return -1


async def wait_for_shutdown_instruction():
    while True:
        if exists("/tmp/.allow-force-stop"):
            logging.info("Received 'allow-force-stop' instruction")
            return
        if exists("/tmp/.allow-stop"):
            logging.info("Received 'allow-stop' instruction")
            return

        logging.info("Waiting for shutdown instruction...")
        await asyncio.sleep(5)


async def shutdown():
    global exiting
    while not (exiting or is_wrong_generation()):
        await asyncio.sleep(1)
        continue

    logging.warning("Received signal, stopping all processes")
    exiting = True  # multiple entry points of shutdown, exiting is global check for various conditions

    if MODE not in ["drivers-loader", "discovery"]:
        if MODE in ["client", "s3", "nfs", "drive", "compute"]:
            await wait_for_shutdown_instruction()

        force_stop = False
        if exists("/tmp/.allow-force-stop"):
            force_stop = True
        if is_wrong_generation():
            force_stop = True
        if MODE not in ["s3", "drive", "compute", "nfs"]:
            force_stop = True
        stop_flag = "--force" if force_stop else "-g"
        await run_command(f"weka local stop {stop_flag}", capture_stdout=False)
        logging.info("finished stopping weka container")
        sys.exit(1)

    if MODE == "drive":
        timeout = 60
        # print out in-kernel devices for up to 60 seconds every 0.3 seconds
        requested_drives = len(await get_requested_drives())
        for _ in range(int(timeout/0.3)):
            drives = await find_weka_drives()
            logging.info(f"Found {len(drives)}: {drives}")
            if len(drives) == requested_drives:
                logging.info("all drives returned to kernel")
                break
            await asyncio.sleep(0.3)

    for key, process in dict(processes.items()).items():
        logging.info(f"stopping process {process.pid}, {key}")
        await stop_process(process)
        logging.info(f"process {process.pid} stopped")

    tasks = [t for t in asyncio.all_tasks(loop) if t is not asyncio.current_task(loop)]
    [task.cancel() for task in tasks]

    logging.info("All processes stopped, stopping main loop")
    loop.stop()
    logging.info("Main loop stopped")


exiting = False


def signal_handler(sig):
    global exiting
    logging.info(f"Received signal {sig}")
    exiting = True


def reap_zombies():
    # agent leaves zombies behind on weka local start
    while True:
        time.sleep(1)
        try:
            # Wait for any child process, do not block
            pid, _ = os.waitpid(-1, os.WNOHANG)
            if pid == 0:  # No zombie to reap
                continue
        except ChildProcessError:
            # No child processes
            continue


zombie_collector = threading.Thread(target=reap_zombies, daemon=True)
zombie_collector.start()

# Setup signal handler for graceful shutdown
loop.add_signal_handler(signal.SIGINT, partial(signal_handler, "SIGINT"))
loop.add_signal_handler(signal.SIGTERM, partial(signal_handler, "SIGTERM"))

shutdown_task = loop.create_task(shutdown())
takeover_shutdown_task = loop.create_task(takeover_shutdown())

main_loop = loop.create_task(main())
logrotate_task = loop.create_task(periodic_logrotate())

try:
    try:
        loop.run_until_complete(main_loop)
        loop.run_forever()
    except RuntimeError:
        if exiting:
            logging.info("Cancelled")
        else:
            raise
    except Exception as e:
        debug_sleep = int(os.environ.get("WEKA_OPERATOR_DEBUG_SLEEP", 3))
        logging.error(f"Error: {e}, sleeping for {debug_sleep} seconds to give a chance to debug")
        time.sleep(debug_sleep)
        raise
finally:
    if _server is not None:
        _server.close()
    logging.info(
        "3 seconds exit-sleep")  # TODO: Remove this once theory of drives not releasing due to sync, confirmed, assuming that sync will happen within 3 seconds
    time.sleep(3)
