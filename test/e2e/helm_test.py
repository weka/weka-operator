import json
from functools import partial

import pytest
from plumbum import local

from .models.kubernetes import Namespace
from .utils import eventually


@pytest.fixture(scope="session")
def helm():
    return local["helm"]


@pytest.fixture(scope="session", autouse=True)
def helm_upgrade(helm, system_namespace: Namespace):
    list_output = helm(
        "list", "--output", "json", "--namespace", system_namespace.metadata.name
    )
    releases = json.loads(list_output)
    try:
        weka_operator_release = next(
            release for release in releases if release["name"] == "weka-operator"
        )
        if weka_operator_release:
            return
    except StopIteration:
        pass

    output = helm(
        "upgrade",
        "--output",
        "json",
        "--create-namespace",
        "--namespace",
        system_namespace.metadata.name,
        "--install",
        "weka-operator",
        # "oci://quay.io/weka.io/helm/weka-operator",
        "charts/weka-operator",
        "--values",
        "charts/weka-operator/values.yaml",
        "--set",
        "prefix=weka-operator,image.repository=quay.io/weka.io/weka-operator,image.tag=latest",
    )
    assert "Release" in output


@pytest.fixture(scope="session", autouse=True)
def image_pull_secret(helm_upgrade, v1_client, system_namespace: Namespace):
    try:
        return v1_client.read_namespaced_secret(
            namespace=system_namespace.metadata.name, name="quay-cred"
        )
    except Exception:
        pass

    default_ns_secret = v1_client.read_namespaced_secret(
        namespace="default", name="quay-cred"
    )
    if not default_ns_secret:
        raise ValueError("quay-cred secret not found in default namespace")

    e2e_ns_secret = v1_client.create_namespaced_secret(
        namespace=system_namespace.metadata.name,
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
def helm_version(helm):
    return helm("version")


@pytest.fixture(scope="session")
def releases(helm, system_namespace):
    output = helm(
        "list", "--output", "json", "--namespace", system_namespace.metadata.name
    )
    assert "weka-operator" in output
    assert "WARNING" not in output

    listing = json.loads(output)
    return listing


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
