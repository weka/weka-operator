import json
import logging
import os
import sys
import subprocess
import time
from typing import List, Optional, Tuple

INSTRUCTIONS = os.environ.get("INSTRUCTIONS", "")

AWS_VENDOR_ID = "1d0f"
AWS_DEVICE_ID = "cd01"

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


def run_command(cmd: str, log_output=True) -> Tuple[Optional[bytes], Optional[bytes], int]:
    logging.debug("Running command: %s", cmd)
    
    process = subprocess.Popen(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    # wait for the process to terminate
    stdout, stderr = process.communicate()

    logging.info(f"Command {cmd} finished with code {process.returncode}")
    if stdout and log_output:
        logging.debug(f"Command {cmd} stdout: {stdout.decode('utf-8')}")
    if stderr and log_output:
        logging.debug(f"Command {cmd} stderr: {stderr.decode('utf-8')}")

    return stdout, stderr, process.returncode


def get_devices_by_id() -> List[str]:
    stdout, stderr, ec = run_command("nsenter --target 1 --mount --pid -- sh -c \"ls -1 /dev/disk/by-id/\"")
    if ec != 0:
        raise Exception(f"Failed to list devices by id: {stderr}")
    return stdout.decode().strip().split("\n")


def get_devices_by_path() -> List[str]:
    stdout, stderr, ec = run_command("nsenter --target 1 --mount --pid -- sh -c \"ls -1 /dev/disk/by-path/\"")
    if ec != 0:
        raise Exception(f"Failed to list devices by path: {stderr}")
    return stdout.decode().strip().split("\n")


def find_weka_drives():
    drives = []
    # ls /dev/disk/by-path/pci-0000\:03\:00.0-scsi-0\:0\:3\:0  | ssd

    devices_by_id = get_devices_by_id()
    devices_by_path = get_devices_by_path()

    part_names = []
    def resolve_to_part_name():
        # TODO: A bit dirty, consolidate paths
        for device in devices_by_path:
            try:
                part_name = subprocess.check_output(f"basename $(readlink -f /dev/disk/by-path/{device})", shell=True).decode().strip()
            except subprocess.CalledProcessError:
                logging.error(f"Failed to get part name for {device}")
                continue
            part_names.append(part_name)
        for device in devices_by_id:
            try:
                part_name = subprocess.check_output(f"basename $(readlink -f /dev/disk/by-id/{device})", shell=True).decode().strip()
                if part_name in part_names:
                    continue
            except subprocess.CalledProcessError:
                logging.error(f"Failed to get part name for {device}")
                continue
            part_names.append(part_name)
    resolve_to_part_name()

    logging.info(f"All found in kernel block devices: {part_names}")
    for part_name in part_names:
        try:
            type_id = subprocess.check_output(f"blkid -s PART_ENTRY_TYPE -o value -p /dev/{part_name}",
                                              shell=True).decode().strip()
        except subprocess.CalledProcessError:
            logging.error(f"Failed to get PART_ENTRY_TYPE for {part_name}")
            continue

        if type_id == "993ec906-b4e2-11e7-a205-a0a8cd3ea1de":
            # TODO: Read and populate actual weka guid here
            weka_guid = ""
            # resolve block_device to serial id
            pci_device_path = subprocess.check_output(f"readlink -f /sys/class/block/{part_name}",
                                                      shell=True).decode().strip()
            if "nvme" in part_name:
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
                "partition": "/dev/" + part_name,
                "block_device": device_path,
                "serial_id": serial_id,
                "weka_guid": weka_guid
            })

    return drives


def write_results(results):
    logging.info("Writing result into /weka-runtime/results.json, results: \n%s", results)
    os.makedirs("/weka-runtime", exist_ok=True)
    with open("/weka-runtime/results.json.tmp", "w") as f:
        json.dump(results, f)
    os.rename("/weka-runtime/results.json.tmp", "/weka-runtime/results.json")


def discover_drives():
    drives = find_weka_drives()
    write_results(dict(
        err=None,
        drives=drives,
    ))


def sign_drives_by_pci_info(vendor_id: str, device_id: str) -> List[str]:
    logging.info("Signing drives. Vendor ID: %s, Device ID: %s", vendor_id, device_id)

    if not vendor_id or not device_id:
        raise ValueError("Vendor ID and Device ID are required")

    cmd = f"lspci -d {vendor_id}:{device_id}" + " | sort | awk '{print $1}'"
    stdout, stderr, ec = run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to list drives: {stderr}")
    if stderr:
        raise Exception(f"lspci stderr: {stderr}")

    signed_drives = []
    pci_devices = stdout.decode().strip().split()

    logging.info(f"Found pci devices: {pci_devices}")

    for pci_device in pci_devices:
        device = f"/dev/disk/by-path/pci-0000:{pci_device}-nvme-1"
        stdout, stderr, ec = run_command(f"nsenter --target 1 --mount --pid -- sh -c \"/weka-sign-drive {device}\"")
        if ec != 0:
            logging.error(f"Failed to sign drive {pci_device} - ec: {ec}, stderr: {stderr}")
            continue
        signed_drives.append(device)
        # TODO: Find serial id already here? it is adhocy non mandatory operation, so does not make sense to populate some db at this point, despite us having info
    return signed_drives


def sign_not_mounted() -> List[str]:
    """
    [root@wekabox18 sdc] 2024-08-21 19:14:58 $ cat /run/udev/data/b`cat /sys/block/sdc/dev` | grep ID_SERIAL=
        E:ID_SERIAL=TOSHIBA_THNSN81Q92CSE_86DS107ATB4V
    :return:
    """
    logging.info("Signing drives not mounted")
    stdout, stderr, ec = run_command("lsblk -o NAME,TYPE,MOUNTPOINT")
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
            stdout, stderr, ec = run_command(f"nsenter --target 1 --mount --pid -- sh -c \"/weka-sign-drive {device}\"")
            if ec != 0:
                logging.error(f"Failed to sign drive {device}: {stderr}")
                continue
            signed_drives.append(device)
    return signed_drives


def sign_device_paths(devices_paths) -> List[str]:
    signed_drives = []
    for device_path in devices_paths:
        stdout, stderr, ec = run_command(f"nsenter --target 1 --mount --pid -- sh -c \"/weka-sign-drive {device_path}\"")
        if ec != 0:
            logging.error(f"Failed to sign drive {device_path}: {stderr}")
            continue
        signed_drives.append(device_path)

        logging.info(f"Signed drive {device_path}")
        logging.info(f"weka-sign-drive output: {stdout}")

    return signed_drives


def sign_drives(instruction: dict) -> List[str]:
    type = instruction['type']
    if type == "aws-all":
        return sign_drives_by_pci_info(
            vendor_id=AWS_VENDOR_ID,
            device_id=AWS_DEVICE_ID,
        )
    elif type == "device-identifiers":
        return sign_drives_by_pci_info(
            vendor_id=instruction.get('pciDevices', {}).get('vendorId'),
            device_id=instruction.get('pciDevices', {}).get('deviceId'),
        )
    elif type == "all-not-root":
        return sign_not_mounted()
    elif type == "device-paths":
        return sign_device_paths(instruction['devicePaths'])
    else:
        raise ValueError(f"Unknown instruction type: {type}")


if __name__ == "__main__":
    logging.info("INSTRUCTIONS: %s", INSTRUCTIONS)

    instruction = json.loads(INSTRUCTIONS)
    if instruction.get('type') and instruction['type'] == 'sign-drives':
        payload = json.loads(instruction['payload'])
        signed_drives = sign_drives(payload)
        logging.info(f"signed_drives: {signed_drives}")
        discover_drives()
    else:
        logging.error("Unknown instruction type: %s", instruction)
    
    # Sleep for a long time to keep the container running
    time.sleep(60 * 60 * 24)   # 24 hours
