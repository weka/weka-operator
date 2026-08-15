from typing import Optional

from dagger import dag, Container, Directory, Socket, Secret


# NOTE: `version` must match the `go` directive in go.mod. CI runs with
# GOTOOLCHAIN=local, so an older builder cannot auto-upgrade and the build
# fails with "go.mod requires go >= <x> (running go <y>)".
async def _go_builder_container(sock: Socket, gh_token: Optional[Secret] = None, version: str = "1.26.6-alpine") -> Container:
    """
    Returns a container suitable for building go applications.
    If gh_token is provided, it will be used to configure git to use the token.
    If gh_token is not provided, sock is used to configure git to use the ssh key.
    """
    cont = (
        dag.container()
        .from_(f"golang:{version}")
        .with_env_variable("GOPRIVATE", "github.com/weka")  # find a way to remove this to be less weka-specific?
    )

    if gh_token:
        cont = (
            cont
            .with_secret_variable("GH_TOKEN", gh_token)
            .with_exec(["sh", "-ec", """
apk add --no-cache git bash
git config --global url."https://x-access-token:$GH_TOKEN@github.com/".insteadOf "https://github.com/"
            """])
        )
    else:
        cont = (
            cont
            .with_exec(["sh", "-ec", """
apk add --no-cache git bash
apk --no-cache add ca-certificates git openssh-client
git config --global url."git@github.com:".insteadOf "https://github.com/"
mkdir -p -m 0700 ~/.ssh && ssh-keyscan github.com >> ~/.ssh/known_hosts
chmod 600 ~/.ssh/known_hosts
export GIT_SSH_COMMAND="ssh -v"
            """])
            .with_unix_socket("/tmp/ssh-agent.sock", sock)
            .with_env_variable("SSH_AUTH_SOCK", "/tmp/ssh-agent.sock")
        )

    cont = (
        cont
        .with_mounted_cache("/go/pkg/mod", dag.cache_volume("go-cache"))
        .with_mounted_cache("/root/.cache/go-build", dag.cache_volume("go-cache-root"))
    )
    return cont


async def helm_builder_container(sock: Socket, gh_token: Optional[Secret] = None) -> Container:
    cont = await _go_builder_container(sock, gh_token)
    return (
        cont
        .with_exec(["apk", "--no-cache", "add", "helm", "make"])
    )

async def helm_runner_container() -> Container:
    return (
        dag.container()
        .from_("alpine:latest")
        .with_exec(["apk", "--no-cache", "add", "helm", "kubectl"])
    )


async def build_go(
        src: Directory,
        sock: Socket,
        gh_token: Optional[Secret] = None,
        cache_deps: bool = True,
        program_path: str = "main.go",
        go_generate: bool = False,
        target_os: str = "",
        target_arch: str = "",
) -> Container:
    """returns container suitable for building go applications"""

    cont = (
        (await _go_builder_container(sock, gh_token))
        .with_file("/src/go.mod", src.file("go.mod"))
        .with_file("/src/go.sum", src.file("go.sum"))
        .with_workdir("/src")
    )

    if cache_deps:
        cont = cont.with_exec(["go", "mod", "download"])

    if go_generate:
        cont = cont.with_exec(["go", "generate", "./..."])

    cont = cont.with_directory("/src", src)

    if target_os and target_arch:
        cont = (cont
            .with_env_variable("CGO_ENABLED", "0")
            .with_env_variable("GOOS", target_os)
            .with_env_variable("GOARCH", target_arch)
        )

    cont = cont.with_exec(["go", "build", "-o", "/out-binary", program_path])
    return await cont


async def build_go_multiple(
        src: Directory,
        sock: Socket,
        gh_token: Optional[Secret] = None,
        cache_deps: bool = True,
        programs: Optional[dict] = None,
        go_generate: bool = False,
        target_os: str = "",
        target_arch: str = "",
) -> Container:
    """Builds multiple go binaries in ONE builder pass so they share the module/build cache and any
    `go generate` output. `programs` maps binary name -> build target. A target is either a plain
    string (a package path like `./cmd/weka-capacity`, or a `main.go` file path), or a dict
    `{"target": <path>, "extra_args": [...]}` to pass per-binary go-build flags (e.g. `-ldflags`,
    `-trimpath`) without affecting the other binaries built in the same pass. Each binary is
    emitted at `/out/<name>`. Mirrors build_go's step order (mod download -> generate -> full
    source -> build)."""
    programs = programs or {}

    cont = (
        (await _go_builder_container(sock, gh_token))
        .with_file("/src/go.mod", src.file("go.mod"))
        .with_file("/src/go.sum", src.file("go.sum"))
        .with_workdir("/src")
    )

    if cache_deps:
        cont = cont.with_exec(["go", "mod", "download"])

    if go_generate:
        cont = cont.with_exec(["go", "generate", "./..."])

    cont = cont.with_directory("/src", src)

    if target_os and target_arch:
        cont = (cont
            .with_env_variable("CGO_ENABLED", "0")
            .with_env_variable("GOOS", target_os)
            .with_env_variable("GOARCH", target_arch)
        )

    for name, target in programs.items():
        if isinstance(target, dict):
            target_path = target["target"]
            extra_args = target.get("extra_args", [])
        else:
            target_path = target
            extra_args = []
        cont = cont.with_exec(["go", "build", *extra_args, "-o", f"/out/{name}", target_path])
    return await cont


async def _uv_base() -> Container:
    # The ghcr.io/astral-sh/uv:alpine image already contains Python and uv
    return (
        dag.container()
        .from_("ghcr.io/astral-sh/uv:alpine")
        .with_exec(["apk", "add", "--no-cache", "bash"])  # Add bash for script execution
    )
