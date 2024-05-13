from functools import partial

from plumbum import local

from ..models.kubernetes import Condition
from ..models.weka_cluster import WekaContainer


class ContainerService:
    def __init__(self, namespace: str):
        self.kubectl = partial(
            local["kubectl"], "--output", "json", "--namespace", namespace
        )

    def get_container(self, name: str):
        output = self.kubectl("get", "wekacontainer", name)
        container = WekaContainer.model_validate_json(output)
        return container

    def wait_for_condition(self, container, condition, timeout="10m") -> Condition:
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
