from typing import Optional

import dagger
from dagger import dag, Container, Directory, Socket, Secret

from containers.builders import build_go


async def operator_ubi(src: Directory, sock: Socket, gh_token: Optional[Secret] = None) -> Container:
    """Returns container suitable for building go applications"""
    operator = await build_go(src, sock, gh_token, cache_deps=False, program_path="cmd/manager/main.go",
                              go_generate=True)

    return await (
        dag.container()
        .from_(
            "registry.access.redhat.com/ubi9/ubi@sha256:9ac75c1a392429b4a087971cdf9190ec42a854a169b6835bc9e25eecaf851258")
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
    """Returns container suitable for building go applications"""
    operator = await operator_ubi(src, sock, gh_token)
    # Compute a compact version by hashing combined digests
    version = await _calc_operator_version(src, version)

    return await operator.publish(f"{repository}:{version}")


async def publish_operator_helm_chart(src: Directory, sock: Socket, repository: str, version: str = "",
                                      gh_token: Optional[Secret] = None) -> str:
    from containers.builders import helm_builder_container

    version = await _calc_operator_version(src, version)

    await (
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
        .with_exec(["sh", "-ec", f"""
        CHART=$(ls charts/weka-operator-*.tgz)
        helm push "$CHART" oci://{repository}
"""])
        .stdout()
    )
    return f"{repository}/weka-operator:{version}"


async def install_helm_chart(image: str, kubeconfig: dagger.Secret, operator_repo: str, values_file: Optional[dagger.File] = None) -> str:
    from containers.builders import helm_runner_container
    repo, _, version = image.rpartition(":")

    # TODO: Add pre-load?
    cont = await (
        (await helm_runner_container())
    )
    if values_file is not None:
        cont = cont.with_file("/values.yaml", values_file)

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
