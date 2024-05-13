from functools import partial
from pathlib import Path
from typing import ContextManager

from plumbum import local

from ..models.kubernetes import Condition
from ..models.weka_cluster import WekaCluster, WekaContainer, WekaContainerList


class ClusterService:
    namespace: str

    def __init__(self, namespace: str):
        self.namespace = namespace
        self.kubectl = partial(
            local["kubectl"], "--output", "json", "--namespace", namespace
        )

    def get_cluster(self, cluster_manifest: Path) -> WekaCluster:
        stdout = self.kubectl("get", "-f", cluster_manifest.absolute())
        cluster = WekaCluster.model_validate_json(stdout)

        return cluster

    def wait_for_condition(self, cluster, condition, timeout="5m") -> Condition:
        output = self.kubectl(
            "wait",
            "wekacluster",
            cluster.metadata.name,
            f"--for=condition={condition}",
            f"--timeout={timeout}",
        )
        cluster = WekaCluster.model_validate_json(output)
        assert cluster.status.conditions
        assert len(cluster.status.conditions) > 0

        types = [c.type for c in cluster.status.conditions]
        assert condition in types

        return [c for c in cluster.status.conditions if c.type == condition][0]

    def wait_for_container_condition(
        self, container, condition, timeout="10m"
    ) -> Condition:
        output = self.kubectl(
            "wait",
            "wekacontainer",
            container.metadata.name,
            f"--for=condition={condition}",
            f"--timeout={timeout}",
        )
        container = WekaContainer.model_validate_json(output)
        assert container.status.conditions
        assert len(container.status.conditions) > 0

        types = [c.type for c in container.status.conditions]
        assert condition in types

        return [c for c in container.status.conditions if c.type == condition][0]

    def get_containers(self, cluster: WekaCluster) -> WekaContainerList:
        output = self.kubectl("get", "wekacontainer")
        all_containers = WekaContainerList.model_validate_json(output)
        return WekaContainerList(
            items=[
                container
                for container in all_containers.items
                if container.metadata.ownerReferences[0].name == cluster.metadata.name
            ]
        )

    def get_drive_containers(self, cluster: WekaCluster) -> WekaContainerList:
        cluster_containers = self.get_containers(cluster)
        return WekaContainerList(
            items=[
                container
                for container in cluster_containers.items
                if container.spec.mode == "drive"
            ]
        )

    def get_compute_containers(self, cluster: WekaCluster) -> WekaContainerList:
        cluster_containers = self.get_containers(cluster)
        return WekaContainerList(
            items=[
                container
                for container in cluster_containers.items
                if container.spec.mode == "compute"
            ]
        )
