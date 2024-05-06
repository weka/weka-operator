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


def test_pods_created_condition(pods_created_condition: Condition):
    assert pods_created_condition.status == "True"
    assert pods_created_condition.type == "PodsCreated"


def test_secrets_created_condition(secrets_created_condition: Condition):
    assert secrets_created_condition.type == "ClusterSecretsCreated"
    assert secrets_created_condition.status == "True"


def test_containers_created(containers):
    assert len(containers.items) == 10


def test_drive_containers_created(drive_containers):
    assert len(drive_containers.items) == 5


def test_compute_containers_created(compute_containers):
    assert len(compute_containers.items) == 5


def drive_container_names():
    return [f"mbpk8soci-sample-drive-{i}" for i in range(5)]


def container_names():
    return [f"mbpk8soci-sample-compute-{i}" for i in range(5)] + drive_container_names()


@pytest.fixture
def container_service(namespace: str) -> ContainerService:
    return ContainerService(namespace)


@pytest.fixture
def container(request, container_service: ContainerService) -> WekaContainer:
    name = request.param
    container = container_service.get_container(name)
    return container


@pytest.mark.parametrize("container", container_names(), indirect=True)
@pytest.mark.parametrize("condition_name", ["EnsuredDrivers", "JoinedCluster"])
def test_container_condition_status_is_true(
    container_service: ContainerService, container: WekaContainer, condition_name: str
):
    assert container.metadata.name.startswith("mbpk8soci-sample-")
    condition = container_service.wait_for_condition(container, condition_name)
    assert condition.status == "True"


@pytest.mark.parametrize("container", drive_container_names(), indirect=True)
def test_drives_added_condition(
    container_service: ContainerService, container: WekaContainer
):
    assert container.metadata.name.startswith("mbpk8soci-sample-drive-")
    condition = container_service.wait_for_condition(container, "DrivesAdded")
    assert condition.status == "True"


@pytest.fixture
def condition(request, cluster_service, weka_cluster) -> Condition:
    return cluster_service.wait_for_condition(weka_cluster, request.param)


@pytest.mark.parametrize(
    "condition",
    [
        "PodsReady",
        "ClusterSecretsApplied",
        "ClusterCreated",
        "DrivesAdded",
        "IoStarted",
    ],
    indirect=True,
)
def test_cluster_startup_condition_status_is_true(condition: Condition):
    assert condition.status == "True"
