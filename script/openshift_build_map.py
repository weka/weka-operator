#!/usr/bin/env python3
import json
import logging
import subprocess

import yaml

versions = [
    "4.16.3",
    "4.16.2",
    "4.16.1",
    "4.16.0",
    "4.15.18",
    "4.15.17",
    "4.15.16",
    "4.15.15",
    "4.15.14",
    "4.15.13",
    "4.15.12",
    "4.15.11",
    "4.15.10",
    "4.15.9",
    "4.15.8",
    "4.15.7",
    "4.15.6",
    "4.15.5",
    # "4.15.4", # This version is not available
    "4.15.3",
    "4.15.2",
    # "4.15.1", # This version is not available
    "4.15.0",
    "4.14.24",
    "4.14.23",
    "4.14.22",
]


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


def main():
    setup_basic_logging()

    for version in versions:
        command = f"""
oc adm release info {version} -o json | jq -r '.displayVersions["machine-os"].Version'
    """
        stdout, stderr, returncode = run_command(command)
        if returncode != 0:
            logging.error(f"Error running command: {command}")
            logging.error(f"stderr: {stderr}")
            return
        machine_os_version = stdout.decode("utf-8").strip()
        logging.info(f"{version} {machine_os_version}")

        command = f"""
oc adm release info {version} -o json | jq '.references.spec.tags'
"""
        stdout, stderr, returncode = run_command(command)
        if returncode != 0:
            logging.error(f"Error running command: {command}")
            logging.error(f"stderr: {stderr}")
            return
        tags = stdout.decode("utf-8").strip()
        tags = json.loads(tags)
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


    data = machine_os_to_driver_toolkit_image

    config_map = {
        "apiVersion": "v1",
        "kind": "ConfigMap",
        "metadata": {
            "name": "driver-toolkit-images",
        },
        "data": data
    }

    # Convert the dictionary to a YAML formatted string
    config_map_yaml = yaml.dump(config_map, default_flow_style=False)
    # Print the YAML to stdout
    print(config_map_yaml)


if __name__ == "__main__":
    main()
