import json
import logging
import os
import sys
import threading
import time
from functools import lru_cache, partial
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
    for driver in "wekafsio wekafsgw igb_uio mpin_user uio_pci_generic".split():
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


WEKA_DRIVER_VERSION = "1.0.0-995f26b334137fd78d57c264d5b19852-GW_aedf44a11ca66c7bb599f302ae1dff86"
IGB_UIO_DRIVER_VERSION = "weka1.0.2"
MPIN_USER_DRIVER_VERSION = "1.0.1"
UIO_PCI_GENERIC_DRIVER_VERSION = "5f49bb7dc1b5d192fb01b442b17ddc0451313ea2"


async def load_drivers():
    stdout, stderr, ec = await run_command(dedent(f"""
        curl -fo /opt/weka/dist/drivers/weka_driver-wekafsgw-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsgw-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        curl -fo /opt/weka/dist/drivers/weka_driver-wekafsio-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/weka_driver-wekafsio-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        curl -fo /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        curl -fo /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        curl -fo /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko {DIST_SERVICE}/dist/v1/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko

        lsmod | grep wekafsgw || insmod /opt/weka/dist/drivers/weka_driver-wekafsgw-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko 
        lsmod | grep wekafsio || insmod /opt/weka/dist/drivers/weka_driver-wekafsio-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        lsmod | grep uio || modprobe uio
        lsmod | grep igb_uio || insmod /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        lsmod | grep mpin_user || insmod /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        lsmod | grep uio_pci_generic || insmod /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
        echo "drivers_loaded"  > /tmp/weka-drivers-loader
    """))
    if ec != 0:  # It is fine to abort like this, as we expect to have all drivers to present on dist service already
        raise Exception(f"Failed to load drivers: {stderr}")


async def copy_drivers():
    stdout, stderr, ec = await run_command(dedent(f"""
      cp /opt/weka/data/weka_driver/{WEKA_DRIVER_VERSION}/`uname -r`/wekafsio.ko /opt/weka/dist/drivers/weka_driver-wekafsio-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
      cp /opt/weka/data/weka_driver/{WEKA_DRIVER_VERSION}/`uname -r`/wekafsgw.ko /opt/weka/dist/drivers/weka_driver-wekafsgw-{WEKA_DRIVER_VERSION}-`uname -r`.`uname -m`.ko

      cp /opt/weka/data/igb_uio/{IGB_UIO_DRIVER_VERSION}/`uname -r`/igb_uio.ko /opt/weka/dist/drivers/igb_uio-{IGB_UIO_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
      cp /opt/weka/data/mpin_user/{MPIN_USER_DRIVER_VERSION}/`uname -r`/mpin_user.ko /opt/weka/dist/drivers/mpin_user-{MPIN_USER_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
      cp /opt/weka/data/uio_generic/{UIO_PCI_GENERIC_DRIVER_VERSION}/`uname -r`/uio_pci_generic.ko /opt/weka/dist/drivers/uio_pci_generic-{UIO_PCI_GENERIC_DRIVER_VERSION}-`uname -r`.`uname -m`.ko
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
    while True:
        _, _, ec = await run_command("weka local ps")
        if ec == 0:
            logging.info("Weka-agent started successfully")
            break
        await asyncio.sleep(0.3)
        logging.info("Waiting for weka-agent to start")
    logging.info("agent started")


# Implement the rest of your logic here
import asyncio
import os
import signal

processes = {}


async def ensure_daemon(command, alias=""):
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


async def run_command(command):
    # TODO: Wrap stdout of commands via INFO via logging
    logging.info("Running command: " + command)
    process = await asyncio.create_subprocess_shell("set -e\n" + command,
                                                    stdout=asyncio.subprocess.PIPE,
                                                    stderr=asyncio.subprocess.PIPE)
    stdout, stderr = await process.communicate()
    logging.info(f"Command {command} finished with code {process.returncode}")
    if stdout:
        logging.info(f"Command {command} stdout: {stdout.decode('utf-8')}")
    if stderr:
        logging.info(f"Command {command} stderr: {stderr.decode('utf-8')}")
    return stdout, stderr, process.returncode


loop = asyncio.get_event_loop()


async def create_container():
    full_cores = find_full_cores(NUM_CORES)
    mode_part = ""
    if MODE == "compute":
        mode_part = "--only-compute-cores"
    elif MODE == "drive":
        mode_part = "--only-drives-cores"
    elif MODE == "client":
        mode_part = "--only-frontend-cores"

    core_str = ",".join(map(str, full_cores))
    logging.info(f"Creating container with cores: {core_str}")

    command = dedent(f"""
        weka local setup container --name {NAME} --no-start --disable \
        --core-ids {core_str} --cores {NUM_CORES} {mode_part} \
        --net {NETWORK_DEVICE}  --base-port {PORT} --memory {MEMORY} \
        {f"--join-ips {JOIN_IPS}" if JOIN_IPS else ""} 
    """)
    logging.info(f"Creating container with command: {command}")
    stdout, stderr, ec = await run_command(command)
    if ec != 0:
        raise Exception(f"Failed to create container: {stderr}")
    logging.info("Container created successfully")


async def configure_traces():
    data = dict(enabled=True, ensure_free_space_bytes=int(ENSURE_FREE_SPACE_GB) * 1024 * 1024 * 1024,
                retention_bytes=int(MAX_TRACE_CAPACITY_GB) * 1024 * 1024 * 1024, retention_type="BYTES", version=1)
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
        if MODE in ["compute", "drive", "client"]:
            await create_container()
        else:
            raise NotImplementedError(f"Unsupported mode: {MODE}")

    # reconfigure containers
    logging.info("Container already exists, reconfiguring")
    resources, stderr, ec = await run_command("weka local resources --json")
    if ec != 0:
        raise Exception(f"Failed to get resources: {stderr}")
    resources = json.loads(resources)
    resources['reserve_1g_hugepages'] = False

    full_cores = find_full_cores(NUM_CORES)
    cores_cursor = 0
    for node_id, node in resources['nodes'].items():
        if node['dedicate_core']:
            node['core_id'] = full_cores[cores_cursor]
            # TODO: Cores change was not fully tested yet in practice
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
    cmd = dedent(f"""
        sed -i 's/cgroups_mode=auto/cgroups_mode=none/g' /etc/wekaio/service.conf || true

        IGNORE_DRIVERS="{ignore_driver_flag}"

        sed -i "s/ignore_driver_spec=.*/ignore_driver_spec={ignore_driver_flag}/g" /etc/wekaio/service.conf || true
        sed -i "s/port=14100/port={AGENT_PORT}/g" /etc/wekaio/service.conf || true
        echo '{{"agent": {{"port": \'{AGENT_PORT}\'}}}}' > /etc/wekaio/service.json
    """)
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to configure agent: {stderr}")
    logging.info("Agent configured successfully")


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
    cmd = "weka local start"
    stdout, stderr, ec = await run_command(cmd)
    if ec != 0:
        raise Exception(f"Failed to start dist container: {stderr}")
    logging.info("dist container started")


async def main():
    if MODE == "drivers-loader":
        # self signal to exit
        await load_drivers()
        return

    await configure_persistency()
    # TODO: Configure agent
    await configure_agent()
    await ensure_daemon(f"/usr/sbin/syslog-ng -F -f /etc/syslog-ng/syslog-ng.conf")

    agent_cmd = f"exec /usr/bin/weka --agent --socket-name weka_agent_ud_socket_{AGENT_PORT} &> /tmp/agent.log"
    await ensure_daemon(agent_cmd, alias="agent")
    await await_agent()
    await ensure_weka_version()

    if MODE not in ["dist", "drivers-loader"]:
        await ensure_drivers()

    if MODE == "dist":
        logging.info("dist-service flow")
        await stop_daemon(processes.get("agent"))
        await configure_agent(agent_handle_drivers=True)
        await ensure_daemon(agent_cmd, alias="agent")
        await await_agent()
        await ensure_dist_container()
        await configure_traces()
        await stop_daemon(processes.get("agent"))
        await configure_agent(agent_handle_drivers=False)
        await ensure_daemon(agent_cmd, alias="agent")
        await await_agent()
        await copy_drivers()
        await start_dist_container()
        return

    await ensure_weka_container()
    await configure_traces()
    await start_weka_container()


async def stop_daemon(process):
    logging.info(f"stopping daemon with pid {process.pid} (via process group)")
    pgid = os.getpgid(process.pid)
    os.killpg(pgid, signal.SIGTERM)
    for k, v in list(processes.items()):
        if v == process:
            del processes[k]
    logging.info(f"waiting for process {process.pid} to exit")
    await process.wait()
    logging.info(f"process {process.pid} exited")


async def shutdown():
    while not exiting:
        await asyncio.sleep(1)
        continue

    logging.warning("Received signal, stopping all processes")
    await run_command("weka local stop")
    logging.info("finished stopping weka container")

    tasks = [t for t in asyncio.all_tasks(loop) if t is not asyncio.current_task(loop)]
    [task.cancel() for task in tasks]
    for process in list(processes.values()):
        await stop_daemon(process)
    loop.stop()


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
main_loop = loop.create_task(main())

try:
    loop.run_until_complete(main_loop)
    loop.run_forever()
except RuntimeError:
    if exiting:
        logging.info("Cancelled")
    else:
        raise
except Exception as e:
    logging.error(f"Error: {e}, sleeping for 3 minutes to give a chance to debug")
    time.sleep(180)
    raise
