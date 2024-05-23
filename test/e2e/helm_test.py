import json
from functools import partial

import pytest
from plumbum import local

from .models.kubernetes import Namespace
from .utils import eventually


@pytest.mark.second
class TestHelm:
    def test_helm_version(self, helm_version):
        assert "v3" in helm_version

    def test_release(self, releases: list[dict], system_namespace: Namespace):
        assert len(releases) == 1

        release = releases[0]
        assert release["name"] == "weka-operator"
        assert release["namespace"] == system_namespace.metadata.name
        assert release["status"] == "deployed"

    def test_operator_deployment(self, appsv1_client, system_namespace):
        for attempt in eventually(timeout=300, interval=5):
            with attempt:
                deployments = appsv1_client.read_namespaced_deployment(
                    namespace=system_namespace.metadata.name,
                    name="weka-operator-controller-manager",
                )
                assert deployments.status.available_replicas == 1

    def test_crds(self, ext_v1_client):
        crds = ext_v1_client.list_custom_resource_definition()
        assert len(crds.items) > 0

        crd_names = {
            crd.metadata.name
            for crd in crds.items
            if crd.metadata.name.endswith("weka.io")
        }
        assert "driveclaims.weka.weka.io" in crd_names
        assert "wekaclients.weka.weka.io" in crd_names
        assert "wekaclusters.weka.weka.io" in crd_names
        assert "wekacontainers.weka.weka.io" in crd_names
        assert "tombstones.weka.weka.io" in crd_names
