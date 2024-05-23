import pytest

from .helm_test import *
from .k8s_cluster_test import *
from .models.kubernetes import Condition
from .models.weka_cluster import WekaContainer
from .services.container_service import ContainerService


def drive_container_names():
    return [f"mbpk8soci-sample-drive-{i}" for i in range(5)]


def container_names():
    return [f"mbpk8soci-sample-compute-{i}" for i in range(5)] + drive_container_names()


class TestHappyPath:
    def test_builder(self, driver_builder):
        items = driver_builder["items"]
        container = [item for item in items if item["kind"] == "WekaContainer"][0]
        assert container["kind"] == "WekaContainer"

        service = [item for item in items if item["kind"] == "Service"][0]
        assert service["kind"] == "Service"

    @pytest.mark.parametrize("container", container_names(), indirect=True)
    @pytest.mark.parametrize("condition_name", ["EnsuredDrivers", "JoinedCluster"])
    def test_container_condition_status_is_true(
        self,
        container_service: ContainerService,
        container: WekaContainer,
        condition_name: str,
    ):
        assert container.metadata.name.startswith("mbpk8soci-sample-")
        condition = container_service.wait_for_condition(container, condition_name)
        assert condition.status == "True"

    @pytest.mark.parametrize("container", drive_container_names(), indirect=True)
    def test_drives_added_condition(
        self, container_service: ContainerService, container: WekaContainer
    ):
        assert container.metadata.name.startswith("mbpk8soci-sample-drive-")
        condition = container_service.wait_for_condition(container, "DrivesAdded")
        assert condition.status == "True"

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
    def test_cluster_startup_condition_status_is_true(self, condition: Condition):
        assert condition.status == "True"

    def test_pods_created_condition(self, pods_created_condition: Condition):
        assert pods_created_condition.status == "True"
        assert pods_created_condition.type == "PodsCreated"

    def test_secrets_created_condition(self, secrets_created_condition: Condition):
        assert secrets_created_condition.type == "ClusterSecretsCreated"
        assert secrets_created_condition.status == "True"

    def test_containers_created(self, containers):
        assert len(containers.items) == 10

    def test_drive_containers_created(self, drive_containers):
        assert len(drive_containers.items) == 5

    def test_compute_containers_created(self, compute_containers):
        assert len(compute_containers.items) == 5
