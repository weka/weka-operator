from dagger import dag, Container, Directory, Socket

from containers.builders import build_go


async def operator_ubi(src: Directory, sock: Socket) -> Container:
    """Returns container suitable for building go applications"""
    operator = await build_go(src, sock, cache_deps=False, program_path="cmd/manager/main.go", go_generate=True)

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
        version = f"v999.0.0-{sha[:12]}"
    return version


async def publish_operator(src: Directory, sock: Socket, repository: str, version: str = "") -> str:
    """Returns container suitable for building go applications"""
    operator = await operator_ubi(src, sock)
    # Compute a compact version by hashing combined digests
    version = await _calc_operator_version(src, version)

    return await operator.publish(f"{repository}:{version}")


async def publish_operator_helm_chart(src: Directory, sock: Socket, repository: str, version: str = "") -> str:
    from containers.builders import helm_builder_container

    version = await _calc_operator_version(src, version)

    await (
        (await helm_builder_container(sock))
        .with_directory("/src", src)
        .with_workdir("/src")
        .with_exec(["sh", "-ec", f"""
    make generate
    make rbac
    make crd
    make chart VERSION={version}
        """])
        .with_exec(["sh", "-ec", f"""
        helm push charts/weka-operator-*.tgz oci://{repository}
"""])
        .stdout()
    )
    return f"{repository}:{version}"
