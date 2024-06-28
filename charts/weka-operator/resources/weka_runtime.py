import json
import logging
import os
import sys
import threading
import time
from functools import lru_cache, partial
from os.path import exists
import socket
from textwrap import dedent

MODE = os.environ.get("MODE")
assert MODE != ""
NUM_CORES = int(os.environ.get("CORES", 0))
CORE_IDS = os.environ.get("CORE_IDS", "auto")
NAME = os.environ["NAME"]
NETWORK_DEVICE = os.environ.get("NETWORK_DEVICE", "")
PORT = os.environ.get("PORT", "")
AGENT_PORT = os.environ.get("AGENT_PORT", "")
MEMORY = os.environ.get("MEMORY", "")
JOIN_IPS = os.environ.get("JOIN_IPS", "")
DIST_SERVICE = os.environ.get("DIST_SERVICE")

MAX_TRACE_CAPACITY_GB = os.environ.get("MAX_TRACE_CAPACITY_GB", 10)
ENSURE_FREE_SPACE_GB = os.environ.get("ENSURE_FREE_SPACE_GB", 20)

WEKA_PERSISTENCE_DIR = os.environ.get("WEKA_PERSISTENCE_DIR")

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
    drivers = "wekafsio wekafsgw igb_uio mpin_user".split()
    if version_params.get('uio_pci_generic') is not False:
        drivers.append("uio_pci_generic")
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
    )
assert version_params
if ":4.2" in IMAGE_NAME:
    # 4.2 does not support graceful exit, so rotating images ungracefully
    # note: this might prevent s3 from going down if trying to upgrade 4.2 version with s3 running
    with open("/tmp/.allow-force-stop", 'w') as file:
        file.write('')

# Implement the rest of your logic here
import asyncio
import os
import signal

loop = asyncio.get_event_loop()


async def load_drivers():
    if not version_params.get("weka_drivers_handling"):
        # LEGACY MODE
        weka_driver_version = version_params.get('wekafs')
        cmd = dedent(f"""
            mkdir -p /opt/weka/dist/drivers
            curl -fo /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsgw-{weka_driver_version}-`uname -r`.`uname -m`.ko
            curl -fo /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsio-{weka_driver_version}-`uname -r`.`uname -m`.ko
            curl -fo /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
            curl -fo /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
            {"" if version_params.get('uio_pci_generic') == False else f"curl -fo /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko"}
            lsmod | grep wekafsgw || insmod /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-`uname -r`.`uname -m`.ko
            lsmod | grep wekafsio || insmod /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-`uname -r`.`uname -m`.ko
            lsmod | grep uio || modprobe uio
            lsmod | grep igb_uio || insmod /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
            lsmod | grep mpin_user || insmod /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
            {"" if version_params.get('uio_pci_generic') == False else f"lsmod | grep uio_pci_generic || insmod /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko"}
            echo "drivers_loaded"  > /tmp/weka-drivers-loader
        """)
    else:
        # list directory /opt/weka/dist/version
        # assert single json file and take json filename
        files = os.listdir("/opt/weka/dist/release")
        assert len(files) == 1, Exception(f"More then one release found: {files}")
        version = files[0].partition(".spec")[0]

        cmd = dedent(f"""
        weka driver download --from '{DIST_SERVICE}' --without-agent --version {version}
        echo drivers downloaded
        weka driver install --without-agent --version {version}
        echo drivers installed
        echo "drivers_loaded"  > /tmp/weka-drivers-loader
            """)

    logging.info(f"Loading drivers with command: {cmd}")
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:  # It is fine to abort like this, as we expect to have all drivers to present on dist service already
        raise Exception(f"Failed to load drivers: {stderr}")


async def copy_drivers():
    if version_params.get("weka_drivers_handling"):
        return

    weka_driver_version = version_params.get('wekafs')
    assert weka_driver_version

    stdout, stderr, ec = await run_command(dedent(f"""
      mkdir -p /opt/weka/dist/drivers
      cp /opt/weka/data/weka_driver/{weka_driver_version}/`uname -r`/wekafsio.ko /opt/weka/dist/drivers/weka_driver-wekafsio-{weka_driver_version}-`uname -r`.`uname -m`.ko
      cp /opt/weka/data/weka_driver/{weka_driver_version}/`uname -r`/wekafsgw.ko /opt/weka/dist/drivers/weka_driver-wekafsgw-{weka_driver_version}-`uname -r`.`uname -m`.ko

      cp /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/`uname -r`/igb_uio.ko /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
      cp /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/`uname -r`/mpin_user.ko /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
      {"" if version_params.get('uio_pci_generic') == False else f"cp /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/`uname -r`/uio_pci_generic.ko /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko"}
    """))
    if ec != 0:
        logging.info(f"Failed to copy drivers post build {stderr}: exc={ec}")
        raise Exception(f"Failed to copy drivers post build: {stderr}")
    logging.info("done copying drivers")


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


@lru_cache
def find_full_cores(n):
    if CORE_IDS != "auto":
        return list(CORE_IDS.split(","))

    selected_siblings = []

    allowed_cores = parse_cpu_allowed_list()

    for cpu_index in allowed_cores:
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
    agent_timeout = 60
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


async def run_command(command, capture_stdout=True, log_execution=True):
    # TODO: Wrap stdout of commands via INFO via logging
    if log_execution:
        logging.info("Running command: " + command)
    if capture_stdout:
        pipe = asyncio.subprocess.PIPE
    else:
        pipe = None
    process = await asyncio.create_subprocess_shell("set -e\n" + command,
                                                    stdout=pipe,
                                                    stderr=pipe)
    stdout, stderr = await process.communicate()
    if log_execution:
        logging.info(f"Command {command} finished with code {process.returncode}")
    if stdout:
        logging.info(f"Command {command} stdout: {stdout.decode('utf-8')}")
    if stderr:
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

    core_str = ",".join(map(str, full_cores))
    logging.info(f"Creating container with cores: {core_str}")

    # read join secret from if file exists /var/run/secrets/weka-operator/operator-user/password
    join_secret = ""
    if os.path.exists("/var/run/secrets/weka-operator/operator-user/join-secret"):
        with open("/var/run/secrets/weka-operator/operator-user/join-secret") as f:
            join_secret_flag = "--join-secret"
            if MODE == "client":
                join_secret_flag = "--join-token"
            join_secret = f.read().strip()

    net_str = f"--net {NETWORK_DEVICE}"
    if "aws_" in NETWORK_DEVICE:
        devices = NETWORK_DEVICE.split(",")
        devices = [await resolve_aws_net(dev) for dev in devices]
        net_str = " ".join([f"--net {d}" for d in devices])

    command = dedent(f"""
        weka local setup container --name {NAME} --no-start --disable\
        --core-ids {core_str} --cores {NUM_CORES} {mode_part} \
        {net_str}  --base-port {PORT} --memory {MEMORY} \
        {f"{join_secret_flag} {join_secret}" if join_secret else ""} \
        {f"--join-ips {JOIN_IPS}" if JOIN_IPS else ""} \
        {f"--client" if MODE == 'client' else ""}
    """)
    logging.info(f"Creating container with command: {command}")
    stdout, stderr, ec = await run_command(command)
    if ec != 0:
        raise Exception(f"Failed to create container: {stderr}")
    logging.info("Container created successfully")


async def configure_traces():
    data = dict(enabled=True, ensure_free_space_bytes=int(ENSURE_FREE_SPACE_GB) * 1024 * 1024 * 1024,
                retention_bytes=int(MAX_TRACE_CAPACITY_GB) * 1024 * 1024 * 1024, retention_type="BYTES", version=1)
    if MODE == 'dist':
        data['enabled'] = False
        data['retention_bytes'] = 128 * 1024 * 1024 * 1024
    data_string = json.dumps(data)

    command = dedent(f"""
        set -e
        mkdir -p /opt/weka/k8s-scripts
        echo '{data_string}' > /opt/weka/k8s-scripts/dumper_config.json.override
        weka local run mv /opt/weka/k8s-scripts/dumper_config.json.override /data/reserved_space/dumper_config.json.override
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


async def ensure_weka_container():
    current_containers = await get_containers()
    if len(current_containers) == 0:
        logging.info("no pre-existing containers, creating")
        # create container
        if MODE in ["compute", "drive", "client", "s3"]:
            await create_container()
        else:
            raise NotImplementedError(f"Unsupported mode: {MODE}")

    # reconfigure containers
    logging.info("Container already exists, reconfiguring")
    resources, stderr, ec = await run_command("weka local resources --json")
    if ec != 0:
        raise Exception(f"Failed to get resources: {stderr}")
    resources = json.loads(resources)
    if MODE == "s3":
        resources['allow_protocols'] = True
    resources['reserve_1g_hugepages'] = False
    resources['excluded_drivers'] = ["igb_uio"]

    full_cores = find_full_cores(NUM_CORES)
    cores_cursor = 0
    for node_id, node in resources['nodes'].items():
        if node['dedicate_core']:
            node['core_id'] = full_cores[cores_cursor]
            cores_cursor += 1
    # save resources
    with open("/tmp/weka-resources.json", "w") as f:
        json.dump(resources, f)
    # reconfigure containers
    stdout, stderr, ec = await run_command("weka local resources import --force /tmp/weka-resources.json")
    if ec != 0:
        raise Exception(f"Failed to import resources: {stderr}")

    stdout, stderr, ec = await run_command("weka local resources apply --force")
    if ec != 0:
        raise Exception(f"Failed to apply resources {stderr}")


async def start_weka_container():
    stdout, stderr, ec = await run_command("weka local start")
    if ec != 0:
        raise Exception(f"Failed to start container: {stderr}")
    logging.info("finished applying new config")
    logging.info(f"Container reconfigured successfully: {stdout}")


async def configure_persistency():
    if not WEKA_PERSISTENCE_DIR:
        return

    command = dedent(f"""
        mkdir -p /opt/weka-preinstalled
        mount -o bind /opt/weka /opt/weka-preinstalled
        mount -o bind {WEKA_PERSISTENCE_DIR} /opt/weka
        mkdir -p /opt/weka/dist
        mount -o bind /opt/weka-preinstalled/dist /opt/weka/dist

        if [ -d /opt/k8s-weka/boot-level ]; then
            BOOT_DIR=/opt/k8s-weka/boot-level/$(cat /proc/sys/kernel/random/boot_id)
            mkdir -p $BOOT_DIR
            mkdir -p /opt/weka/external-mounts/cleanup
            mount -o bind $BOOT_DIR /opt/weka/external-mounts/cleanup
        fi
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
    sed -i "s@conditional_mounts_ids=.*@conditional_mounts_ids=/etc/hosts,/etc/resolv.conf@g" /etc/wekaio/service.conf || true
    """

    cmd = dedent(f"""
        {drivers_handling_cmd}
        sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true
        sed -i "s/port=14100/port={AGENT_PORT}/g" /etc/wekaio/service.conf || true
        echo '{{"agent": {{"port": \'{AGENT_PORT}\'}}}}' > /etc/wekaio/service.json
    """)
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to configure agent: {stderr}")
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

    if version_params.get("weka_drivers_handling"):
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


async def ensure_dist_container():
    logging.info("ensuring dist container")

    cmd = dedent(f"""
        weka local rm dist --force || true
        weka local setup container --name dist --net udp --base-port {PORT} --no-start --disable
        """)
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to create dist container: {stderr}")

    logging.info("dist container created successfully")
    # wait for container to become running


async def start_dist_container():
    logging.info("starting dist container")
    # stdout, stderr, ec = await run_command(cmd)
    # if ec != 0:
    #     raise Exception(f"Failed to start dist container: {stderr}")
    # ! start_process is deprecated and this is the only place that uses it
    # TODO: Revalidate if it needed or can be simple run_command(As it should be)
    await start_process(
        "weka local start")  # weka local start is not returning, so we need to daemonize it, this is a hack that needs to go away
    # reason of being stuck: agent tries to authenticate using admin:admin into this stem container, for not known reason
    logging.info("dist container started")


async def discovery():
    # TODO: We should move here everything else we need to discover per node
    # This might be a good place to discover drives as well, as long we have some selector to discover by
    with open("/tmp/weka-discovery.json.tmp", "w") as f:
        data = dict(
            is_ht=len(read_siblings_list(0)) > 1
        )
        json.dump(data, f)
    os.rename("/tmp/weka-discovery.json.tmp", "/tmp/weka-discovery.json")
    logging.info("discovery done")


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


SOCKET_NAME = '\0weka_runtime_' + NAME  # Abstract namespace socket
GENERATION_PATH_DIR = '/opt/weka/k8s-runtime'
GENERATION_PATH = f'{GENERATION_PATH_DIR}/runtime-generation'
CURRENT_GENERATION = str(time.time())

def write_generation():
    logging.info("Writing generation %s", CURRENT_GENERATION)
    os.makedirs(GENERATION_PATH_DIR, exist_ok=True)
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
async def main():
    if MODE == "drivers-loader":
        # self signal to exit
        await override_dependencies_flag()
        max_retries = 10
        for i in range(max_retries):
            try:
                await load_drivers()
            except:
                if i == max_retries - 1:
                    raise
                logging.info("retrying drivers download")
                await asyncio.sleep(1)
        return

    if MODE == "discovery":
        # self signal to exit
        await discovery()
        return

    await configure_persistency()
    write_generation() # write own generation to kill other processes
    global _server
    _server = await obtain_lock() # then waiting for lock with short timeout

    # TODO: Configure agent
    await configure_agent()
    syslog = Daemon("/usr/sbin/syslog-ng -F -f /etc/syslog-ng/syslog-ng.conf", "syslog")
    await syslog.start()

    agent_cmd = get_agent_cmd()
    agent = Daemon(agent_cmd, "agent")
    await agent.start()
    await await_agent()

    await ensure_weka_version()
    await override_dependencies_flag()

    if MODE not in ["dist", "drivers-loader", "build"]:
        await ensure_drivers()

    if MODE == "dist":
        logging.info("dist-service flow")
        await agent.stop()
        await configure_agent(agent_handle_drivers=True)
        await agent.start()
        await await_agent()
        await ensure_dist_container()
        await configure_traces()
        await agent.stop()
        await configure_agent(agent_handle_drivers=False)
        await agent.start()
        await await_agent()
        await copy_drivers()
        await start_dist_container()
        await cleanup_traces_and_stop_dumper()
        return

    if MODE == "build":
        logging.info("dist-build flow")
        # await stop_daemon(processes.get("agent"))
        # await configure_agent(agent_handle_drivers=True)
        # await ensure_daemon(agent_cmd, alias="agent")
        # await await_agent()
        await override_dependencies_flag()
        # await configure_traces()
        # await cleanup_traces_and_stop_dumper()
        await asyncio.sleep(600)  # giving 10 minutes for manual hacks until this actually works
        return

    await ensure_weka_container()
    await configure_traces()
    await start_weka_container()


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
        logging.error("No generation detected, ignoring generation")
        return False

    if current_generation != CURRENT_GENERATION:
        logging.error("Wrong generation detected, exiting, current:%s, read: %s", CURRENT_GENERATION, read_generation())
        return True
    return False

async def takeover_shutdown():
    while not is_wrong_generation():
        await asyncio.sleep(1)

    await run_command("weka local stop --force", capture_stdout=False)

async def shutdown():
    global exiting
    while not (exiting or is_wrong_generation()):
        await asyncio.sleep(1)
        continue

    logging.warning("Received signal, stopping all processes")
    exiting = True # multiple entry points of shutdown, exiting is global check for various conditions

    if MODE not in ["drivers-loader", "discovery"]:
        force_stop = False
        if exists("/tmp/.allow-force-stop"):
            force_stop = True
        if is_wrong_generation():
            force_stop = True
        stop_flag = "--force" if force_stop else "-g"
        await run_command(f"weka local stop {stop_flag}", capture_stdout=False)
        logging.info("finished stopping weka container")

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
