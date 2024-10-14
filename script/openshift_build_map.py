#!/usr/bin/env python3
import json
import logging
import subprocess
from joblib import Memory

import yaml

versions = [
    "4.17.0",
    "4.16.16",
    "4.16.15",
    "4.16.14",
    "4.16.13",
    "4.16.12",
    "4.16.11",
    "4.16.10",
    "4.16.9",
    "4.16.8",
    "4.16.7",
    "4.16.6",
    "4.16.5",
    "4.16.4",
    "4.16.3",
    "4.16.2",
    "4.16.1",
    "4.16.0",
    # "4.15.18",
    # "4.15.17",
    # "4.15.16",
    # "4.15.15",
    # "4.15.14",
    # "4.15.13",
    # "4.15.12",
    # "4.15.11",
    # "4.15.10",
    # "4.15.9",
    # "4.15.8",
    # "4.15.7",
    # "4.15.6",
    # "4.15.5",
    # # "4.15.4", # This version is not available
    # "4.15.3",
    # "4.15.2",
    # # "4.15.1", # This version is not available
    # "4.15.0",
    # "4.14.24",
    # "4.14.23",
    # "4.14.22",
]

from joblib import Memory

# Define the directory for caching
memory = Memory("cache", verbose=0)


def run_command(command):
    # TODO: Wrap stdout of commands via INFO via logging
    process = subprocess.Popen("set -e\n" + command,
                               shell=True,
                               stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE
                               )
    stdout, stderr = process.communicate()
    return stdout, stderr, process.returncode


machine_os_to_driver_toolkit_image = {}


def setup_basic_logging():
    logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(levelname)s - %(message)s')


@memory.cache
def get_machine_os_version(version):
    command = f"""
oc adm release info {version} -o json | jq -r '.displayVersions["machine-os"].Version'
    """
    stdout, stderr, returncode = run_command(command)
    if returncode != 0:
        logging.error(f"Error running command: {command}")
        logging.error(f"stderr: {stderr}")
        raise Exception(f"could not get image details for {version}")
    machine_os_version = stdout.decode("utf-8").strip()
    logging.info(f"{version} {machine_os_version}")
    return machine_os_version

@memory.cache
def get_version_tags(version):
    command = f"""
oc adm release info {version} -o json | jq '.references.spec.tags'
"""
    stdout, stderr, returncode = run_command(command)
    if returncode != 0:
        logging.error(f"Error running command: {command}")
        logging.error(f"stderr: {stderr}")
        return
    tags = stdout.decode("utf-8").strip()
    return json.loads(tags)


def main():
    setup_basic_logging()

    for version in versions:
        machine_os_version = get_machine_os_version(version)
        tags = get_version_tags(version)

        current_image = ""
        for tag in tags:
            if tag["name"] == "driver-toolkit":
                current_image = tag["from"]["name"]
                break

        if machine_os_version in machine_os_to_driver_toolkit_image:
            logging.warning(f"Skipping {version} {machine_os_version} as it already exists")
            logging.warning(f"Top version(unknown) used {machine_os_to_driver_toolkit_image[machine_os_version]}, while current version is {current_image}")
            continue

        machine_os_to_driver_toolkit_image[machine_os_version] = current_image.replace("quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:", "")
        machine_os_to_driver_toolkit_image[machine_os_version] = machine_os_to_driver_toolkit_image[machine_os_version]


    data = machine_os_to_driver_toolkit_image

    config_map = {
        "apiVersion": "v1",
        "kind": "ConfigMap",
        "metadata": {
            "name": "ocp-driver-toolkit-images",
        },
        "data": data
    }


    # Custom representer for quoting strings in values only
    def quoted_str_representer(dumper, data):
        return dumper.represent_scalar('tag:yaml.org,2002:str', data, style='"')

    # Register the custom representer for strings
    # yaml.add_representer(str, quoted_str_representer)


# Convert the dictionary to a YAML formatted string
    config_map_yaml = yaml.dump(config_map, default_style='"')
    # Print the YAML to stdout
    print(config_map_yaml)


if __name__ == "__main__":
    main()
