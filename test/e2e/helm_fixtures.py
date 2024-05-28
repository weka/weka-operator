import base64
import json
from functools import partial

import pytest
from plumbum import local

from .models.kubernetes import Namespace


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
def image_pull_secret(helm_upgrade, kubectl, system_namespace: Namespace):
    k_get_secret = partial(kubectl, "get", "secret")
    try:
        result = k_get_secret(
            "quay-cred", "--namespace", system_namespace.metadata.name
        )
        return json.loads(result)
    except Exception:
        pass

    result = k_get_secret("quay-cred", "--namespace", "default")
    default_ns_secret = json.loads(result)

    secret_name = default_ns_secret["metadata"]["name"]

    # `data` is base64 encoded json string, decode and parse it
    secret_data = {
        key: base64.b64decode(value).decode("utf-8")
        for key, value in default_ns_secret["data"].items()
    }
    # credentials are embedded in `.dockerconfigjson` -> `auths` -> `quay.io`
    assert ".dockerconfigjson" in secret_data

    assert isinstance(secret_data[".dockerconfigjson"], str)
    dockerconfigjson = json.loads(secret_data[".dockerconfigjson"])
    assert isinstance(dockerconfigjson, dict)
    assert "auths" in dockerconfigjson
    auths = dockerconfigjson["auths"]

    assert isinstance(auths, dict)
    assert "quay.io" in auths
    credentials = auths["quay.io"]

    assert "username" in credentials
    assert "password" in credentials

    k = local["kubectl"]
    result = k(
        "create",
        "secret",
        "docker-registry",
        secret_name,
        "--docker-server=quay.io",
        "--docker-username",
        credentials["username"],
        "--docker-password",
        credentials["password"],
        "--namespace",
        system_namespace.metadata.name,
    )
    return result


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
