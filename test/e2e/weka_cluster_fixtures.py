import json
from pathlib import Path

import pytest
from plumbum.commands.processes import ProcessExecutionError

from .models.kubernetes import Condition, Namespace, Secret
from .models.weka_cluster import WekaCluster, WekaContainer, WekaContainerList
from .services.cluster_service import ClusterService
from .services.container_service import ContainerService


@pytest.fixture(scope="session")
def cluster_namespace(kubectl, namespace) -> Namespace:
    try:
        stdout = kubectl("get", "namespace", namespace)
        result = Namespace.model_validate_json(stdout)
        if result.metadata.name == namespace:
            return result
    except ProcessExecutionError as e:
        if e.retcode != 1:
            raise
        if (
            e.stderr.strip()
            != f'Error from server (NotFound): namespaces "{namespace}" not found'
        ):
            raise
        pass

    stdout = kubectl("create", "namespace", namespace)
    return Namespace.model_validate_json(stdout)


@pytest.fixture()
def cluster_service(kubectl, cluster_namespace) -> ClusterService:
    namespace = cluster_namespace.metadata.name
    return ClusterService(namespace)


@pytest.fixture(scope="session")
def cluster_manifest() -> Path:
    return Path(__file__).parent / "manifests/oci_cluster.yaml"


@pytest.fixture(scope="session")
def cluster_secret(v1_client, cluster_namespace: Namespace) -> Secret:
    namespace = cluster_namespace.metadata.name
    try:
        return v1_client.read_namespaced_secret(namespace=namespace, name="quay-cred")
    except Exception:
        pass

    default_ns_secret = v1_client.read_namespaced_secret(
        namespace="default", name="quay-cred"
    )
    if not default_ns_secret:
        raise ValueError("quay-cred secret not found in default namespace")

    e2e_ns_secret = v1_client.create_namespaced_secret(
        namespace=namespace,
        body={
            "apiVersion": "v1",
            "kind": "Secret",
            "metadata": {"name": default_ns_secret.metadata.name},
            "type": default_ns_secret.type,
            "data": default_ns_secret.data,
        },
    )
    return e2e_ns_secret


@pytest.fixture(scope="session")
def weka_cluster(
    kubectl,
    cluster_manifest: Path,
    cluster_namespace: Namespace,
    cluster_secret: Secret,
) -> WekaCluster:
    namespace = cluster_namespace.metadata.name
    stdout = kubectl(
        "apply", "--namespace", namespace, "-f", cluster_manifest.absolute()
    )
    cluster = WekaCluster.model_validate_json(stdout)
    assert cluster.metadata.uid is not None

    return cluster


@pytest.fixture
def pods_created_condition(cluster_service, weka_cluster):
    return cluster_service.wait_for_condition(weka_cluster, "PodsCreated")


@pytest.fixture
def secrets_created_condition(cluster_service, weka_cluster) -> Condition:
    return cluster_service.wait_for_condition(weka_cluster, "ClusterSecretsCreated")


@pytest.fixture
def containers(cluster_service: ClusterService, weka_cluster: WekaCluster):
    return cluster_service.get_containers(weka_cluster)


@pytest.fixture
def drive_containers(cluster_service, weka_cluster):
    return cluster_service.get_drive_containers(weka_cluster)


@pytest.fixture
def compute_containers(cluster_service, weka_cluster):
    return cluster_service.get_compute_containers(weka_cluster)


@pytest.fixture
def container_service(namespace: str) -> ContainerService:
    return ContainerService(namespace)


@pytest.fixture
def container(request, container_service: ContainerService) -> WekaContainer:
    name = request.param
    container = container_service.get_container(name)
    return container


@pytest.fixture
def condition(request, cluster_service, weka_cluster) -> Condition:
    return cluster_service.wait_for_condition(weka_cluster, request.param)
