import json
from pathlib import Path

import pytest

from .helm_test import helm, helm_upgrade
from .models.kubernetes import Namespace


@pytest.fixture(scope="session")
def driver_builder_yaml() -> Path:
    return Path(__file__).parent / "manifests/driver_builder.yaml"


@pytest.fixture(scope="session")
def driver_builder(
    kubectl, driver_builder_yaml, system_namespace: Namespace, helm_upgrade
):
    stdout = kubectl("apply", "-f", driver_builder_yaml.absolute())
    result = json.loads(stdout)
    assert "items" in result

    namespace = system_namespace.metadata.name
    kubectl(
        "--namespace",
        namespace,
        "wait",
        "pod",
        "--for=condition=Ready",
        "--timeout=5m",
        "--selector=app=weka-driver-builder",
    )
    stdout = kubectl("get", "-f", driver_builder_yaml.absolute())
    result = json.loads(stdout)
    assert "items" in result
    return result


def test_builder(driver_builder):
    items = driver_builder["items"]
    container = [item for item in items if item["kind"] == "WekaContainer"][0]
    assert container["kind"] == "WekaContainer"

    service = [item for item in items if item["kind"] == "Service"][0]
    assert service["kind"] == "Service"
