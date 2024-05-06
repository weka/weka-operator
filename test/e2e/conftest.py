from functools import partial

import pytest
from kubernetes import client, config
from plumbum import ProcessExecutionError, local

from .models.kubernetes import Namespace


@pytest.fixture(scope="session")
def namespace():
    return "weka-operator-e2e"


@pytest.fixture(autouse=True, scope="session")
def load_kube_config():
    config.load_kube_config()


@pytest.fixture(scope="session")
def kubectl():
    k = local["kubectl"]
    return partial(k, "--output", "json")


@pytest.fixture(scope="session")
def system_namespace(kubectl):
    namespace = "weka-operator-e2e-system"
    try:
        result = kubectl("create", "namespace", namespace)
    except ProcessExecutionError as e:
        if "AlreadyExists" in e.stderr:
            result = kubectl("get", "namespace", namespace)
        else:
            raise e

    return Namespace.model_validate_json(result)


@pytest.fixture(scope="session")
def v1_client():
    return client.CoreV1Api()


@pytest.fixture(scope="session")
def appsv1_client():
    return client.AppsV1Api()


@pytest.fixture(scope="session")
def ext_v1_client():
    return client.ApiextensionsV1Api()


@pytest.fixture(scope="session")
def nodes(v1_client):
    return v1_client.list_node()


@pytest.fixture(scope="session")
def backend_nodes(nodes):
    return [
        node
        for node in nodes.items
        if "weka.io/role" in node.metadata.labels
        and node.metadata.labels["weka.io/role"] == "backend"
    ]


@pytest.fixture(scope="session")
def builder_nodes(nodes):
    return [
        node
        for node in nodes.items
        if "weka.io/role" in node.metadata.labels
        and node.metadata.labels["weka.io/role"] == "builder"
    ]
