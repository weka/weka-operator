import os
from typing import List

import dagger
from dagger import dag, function, object_type

from containers.builders import build_go
from utils.github import GitHubClient


@object_type
class OperatorFlows:
    @function
    def container_echo(self, string_arg: str) -> dagger.Container:
        """Returns a container that echoes whatever string argument is provided"""
        return dag.container().from_("alpine:latest").with_exec(["echo", string_arg])

    @function
    async def grep_dir(self, directory_arg: dagger.Directory, pattern: str) -> str:
        """Returns lines that match a pattern in the files of the provided Directory"""
        return await (
            dag.container()
            .from_("alpine:latest")
            .with_directory("/mnt", directory_arg)
            .with_workdir("/mnt")
            .with_exec(["grep", "-R", pattern, "."])
            .stdout()
        )

    @function
    async def gh_container(self, token: dagger.Secret) -> dagger.Container:
        return await (
            dag.container()
            .from_("ghcr.io/astral-sh/uv:debian")
            .with_exec(["bash", "-ec", """
            (type -p wget >/dev/null || (apt update && apt-get install wget -y)) \
            && mkdir -p -m 755 /etc/apt/keyrings \
                && out=$(mktemp) && wget -nv -O$out https://cli.github.com/packages/githubcli-archive-keyring.gpg \
                && cat $out | tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
            && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
            && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
            && apt update \
            && apt install gh -y
            """])
            .with_secret_variable("GH_TOKEN", token)
        )

    @function
    async def get_pr_content(
            self,
            pr_number: int,
            repository: str,
            gh_token: dagger.Secret
    ) -> str:
        """Returns the description of a pull request"""
        github_client = GitHubClient("weka/weka-operator", await gh_token.plaintext())
        pr = github_client.get_pr_details(pr_number)
        pr_diff = github_client.get_pr_diff(pr_number)

        return f"""
        Title: <title>{pr.title}</title>
        Description: <desc>{pr.body}</desc>
        Diff: <diff>{pr_diff}</diff>
        """

    @function
    async def ai_pr_release_notes(self,
                                  pr_number: int,
                                  repository: str,
                                  gh_token: dagger.Secret
                                  ) -> str:
        body = await self.get_pr_content(
            pr_number=pr_number,
            repository=repository,
            gh_token=gh_token
        )
        return await (
            dag.llm()
            .with_model("gemini-2.5-flash-preview-04-17")
            .with_prompt(
                f"""
                You are analyzing a pull request description to determine if it is user-facing or not.
                Return "Ignore" if not user facing
                Return user-facing release notes if user-facing
                PR:
                
                <body>{body}</body>

                """
            ).last_reply()
        )

    @function
    async def ai_build_test_plan(self,
                                 pr_numbers: List[int],
                                 repository: str,
                                 gh_token: dagger.Secret
                                 ) -> str:
        bodies = []
        for pr_number in pr_numbers:
            body = await self.get_pr_content(
                pr_number=pr_number,
                repository=repository,
                gh_token=gh_token
            )
            bodies.append(body)

        bodies_str = "\n".join(f"<body>{body}</body>" for body in bodies)

        return await (
            dag.llm()
            .with_model("gemini-2.5-flash-preview-04-17")
            .with_prompt(
                f"""
                Produce a test plan for the following pull requests.
                Organize it as a clear step, optimizing for covering multiple pull requests at once
                Respond with complete plan in markdown format
                
                PRs:
                {bodies_str}
                

                """
            ).last_reply()
        )

    @function
    async def publish_dummy(self) -> str:
        return await (
            dag.container()
            .from_("alpine:latest")
            .with_exec(["echo", "dummy"])
            .publish("images.services.scalar:5002/alpine:latest")
        )

    @function
    async def ci_on_merge_queue(self,
                        operator: dagger.Directory,
                        testing: dagger.Directory,
                        wekai: dagger.Directory,
                        sock: dagger.Socket,
                        ) -> dagger.Container:
        from containers.builders import _uv_base
        from apps.operator import publish_operator, publish_operator_helm_chart

        wekai = await build_go(wekai, sock)
        testing = await build_go(testing, sock, cache_deps=False)
        operator_image = await publish_operator(operator, sock, repository="images.services.scalar:5002/weka-operator")
        operator_helm = await publish_operator_helm_chart(operator, sock, repository="images.services.scalar:5002/helm/weka-operator")

        return await (
            (await _uv_base())
            .with_file("/wekai", wekai.file("/out-binary"))
            .with_file("/weka-k8s-testing", testing.file("/out-binary"))
            .with_new_file("/versions", f"{operator_image}\n{operator_helm}")
        )

    @function
    async def test_wekai(self, wekai: dagger.Directory, sock: dagger.Socket) -> dagger.Container:
        from containers.builders import build_go
        return await (
            await build_go(wekai, sock)
        )

    @function
    async def build_operator(self, src: dagger.Directory, sock: dagger.Socket) -> dagger.Container:
        from containers.builders import build_go
        return await (
            await build_go(src, sock, cache_deps=False, program_path="cmd/manager/main.go")
        )