import json
from pathlib import Path

import pytest

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
