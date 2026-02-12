import asyncio
from typing import List, Optional

import dagger
from dagger import dag, Container, Directory, Socket, Secret

from containers.builders import build_go

PLATFORMS = ["linux/amd64", "linux/arm64"]


async def operator_ubi(src: Directory, sock: Socket, gh_token: Optional[Secret] = None,
                       platform: str = "") -> Container:
    """Returns container suitable for building go applications, optionally for a specific platform."""
    target_os, target_arch = "", ""
    if platform:
        target_os, target_arch = platform.split("/")

    operator = await build_go(src, sock, gh_token, cache_deps=False, program_path="cmd/manager/main.go",
                              go_generate=True, target_os=target_os, target_arch=target_arch)

    base = dag.container(platform=dagger.Platform(platform)) if platform else dag.container()
    return await (
        base
        .from_("registry.access.redhat.com/ubi9/ubi")
        .with_file("/weka-operator", operator.file("/out-binary"))
    )


async def _calc_operator_version(src: Directory, version: str = "") -> str:
    if not version:
        digest = await src.digest()
        sha = digest.split(":")[-1]
        version = f"v999.0.0-s{sha[:12]}"
    return version


async def publish_operator(src: Directory, sock: Socket, repository: str, version: str = "",
                           gh_token: Optional[Secret] = None) -> str:
    """Build and publish multi-arch operator image (linux/amd64 + linux/arm64)."""
    version = await _calc_operator_version(src, version)

    variants = await asyncio.gather(*[
        operator_ubi(src, sock, gh_token, platform=p)
        for p in PLATFORMS
    ])

    return await dag.container().publish(
        f"{repository}:{version}",
        platform_variants=list(variants),
    )


async def publish_operator_helm_chart(src: Directory, sock: Socket, repository: str, version: str = "",
                                      gh_token: Optional[Secret] = None,
                                      helm_username: Optional[Secret] = None,
                                      helm_password: Optional[Secret] = None) -> str:
    from containers.builders import helm_builder_container

    version = await _calc_operator_version(src, version)

    container = (
        (await helm_builder_container(sock, gh_token))
        .with_directory("/src", src)
        .with_workdir("/src")
        .with_exec(["sh", "-ec", f"""
    rm -f charts/weka-operator-*.tgz
    make generate
    make rbac
    make crd
    make chart VERSION={version}
        """])
    )

    if helm_username and helm_password:
        registry_host = repository.split("/")[0]
        container = (
            container
            .with_secret_variable("HELM_USERNAME", helm_username)
            .with_secret_variable("HELM_PASSWORD", helm_password)
            .with_exec(["sh", "-ec", f"""
        helm registry login {registry_host} -u "$HELM_USERNAME" -p "$HELM_PASSWORD"
            """])
        )

    await (
        container
        .with_exec(["sh", "-ec", f"""
        CHART=$(ls charts/weka-operator-*.tgz)
        helm push "$CHART" oci://{repository}
"""])
        .stdout()
    )
    return f"{repository}/weka-operator:{version}"


async def install_helm_chart(image: str, kubeconfig: dagger.Secret, operator_repo: str,
                            values_file: Optional[dagger.File] = None,
                            helm_username: Optional[Secret] = None,
                            helm_password: Optional[Secret] = None) -> str:
    from containers.builders import helm_runner_container
    repo, _, version = image.rpartition(":")

    # TODO: Add pre-load?
    cont = await (
        (await helm_runner_container())
    )
    if values_file is not None:
        cont = cont.with_file("/values.yaml", values_file)

    if helm_username and helm_password:
        registry_host = repo.split("/")[0]
        cont = (
            cont
            .with_secret_variable("HELM_USERNAME", helm_username)
            .with_secret_variable("HELM_PASSWORD", helm_password)
            .with_exec(["sh", "-ec", f"""
        helm registry login {registry_host} -u "$HELM_USERNAME" -p "$HELM_PASSWORD"
            """])
        )

    return await (cont
                  .with_mounted_secret("/kubeconfig", kubeconfig)
                  # helm pull to install crds from crd directory
                  .with_env_variable("KUBECONFIG", "/kubeconfig")
                  .with_exec(["sh", "-ec", f"""
        helm show crds oci://{repo} --version {version} | kubectl apply --server-side --force-conflicts -f -
        """
                              ])
                  .with_exec(["sh", "-ec", f"""
        helm upgrade --install weka-operator oci://{repo} --namespace weka-operator-system \
            --version {version} \
            --create-namespace \
            --set image.repository={operator_repo} \
        {"--values /values.yaml" if values_file is not None else ""}
         """])
                  .stdout()
                  )
