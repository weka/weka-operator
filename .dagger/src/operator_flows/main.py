import re
from typing import List
from pathlib import Path

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
    async def _get_pr_content(
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
        body = await self._get_pr_content(
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
            body = await self._get_pr_content(
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
    async def ci_on_merge_queue_env(self,
                        operator: dagger.Directory,
                        testing: dagger.Directory,
                        wekai: dagger.Directory,
                        sock: dagger.Socket,
                        kubeconfig_path: dagger.File,
                        operator_version: str = "",
                        ) -> dagger.Container:
        from containers.builders import _uv_base
        from apps.operator import publish_operator, publish_operator_helm_chart

        wekai = await build_go(wekai, sock)
        testing = await build_go(testing, sock, cache_deps=False)
        operator_image = await publish_operator(operator, sock, repository="images.services.scalar:5002/weka-operator", version=operator_version)
        operator_helm = await publish_operator_helm_chart(operator, sock, repository="images.services.scalar:5002/helm", version=operator_version)

        base_container = await _uv_base()
        base_container = (
            base_container
            .with_file("/wekai", wekai.file("/out-binary"))
            .with_file("/weka-k8s-testing", testing.file("/out-binary"))
            .with_new_file("/versions", f"{operator_image}\n{operator_helm}")
        )

        if kubeconfig_path:
            # Mount local kubeconfig file
            base_container = (
                base_container
                .with_exec(["mkdir", "-p", "/.kube"])
                .with_mounted_file("/.kube/config", kubeconfig_path)
            )
        
        return base_container

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

    def _extract_pr_numbers(self, title: str) -> List[int]:
        """Extracts PR numbers from a title string containing format like '(PRs 1132, 1133,...)'"""
        match = re.search(r'\(PRs\s+([\d,\s]+)\)', title)
        if match:
            numbers_str = match.group(1)
            return [int(num.strip()) for num in numbers_str.split(',')]
        return []

    @function
    async def ci_on_merge_queue_plan(self,
                                     operator: dagger.Directory,
                                     testing: dagger.Directory,
                                     wekai: dagger.Directory,
                                     sock: dagger.Socket,
                                     mq_pr_number: int,
                                     gh_token: dagger.Secret,
                                     openai_api_key: dagger.Secret,
                                     gemini_api_key: dagger.Secret,
                                     kubeconfig_path: dagger.File,
                                     operator_version: str = "",
                                     initial_weka_version: str = "quay.io/weka.io/weka-in-container:4.4.5.95-k8s-safe-stop-and-metrics-alpha",
                                     new_weka_version: str = "quay.io/weka.io/weka-in-container:4.4.5.118-k8s.3",
                                     ) -> str:
        env = await self.ci_on_merge_queue_env(operator, testing, wekai, sock, kubeconfig_path, operator_version)
        
        #TODO: we will execute(?) upgade test in this env, but should it be part of dagger flow? or only prepare images and so?

        gh_client = GitHubClient("weka/weka-operator", await gh_token.plaintext())
        pr = gh_client.get_pr_details(mq_pr_number)
        pr_numbers = self._extract_pr_numbers(pr.title)

        if not pr_numbers:
            raise ValueError(f"PR {mq_pr_number} does not contain any PR numbers in the title")
        
        # call generate_ai_plan_for_prs
        test_artifacts = await self.generate_ai_plan_for_prs(
            operator=operator,
            pr_numbers=pr_numbers,
            gh_token=gh_token,
            openai_api_key=openai_api_key,
        )

        organized_artifacts = (
            dag.container()
            .from_("alpine:latest")
            .with_directory("/operator", operator)
            .with_workdir("/operator")
            .with_directory("/test_artifacts", test_artifacts)
            .with_exec(["script/organize_pr_hooks.sh", "/test_artifacts", "/test_artifacts_organized"])
            .directory("/test_artifacts_organized")
        )
        # organized_artifacts = (
        #     dag.container()
        #     .from_("alpine:latest")
        #     .with_directory("/operator", operator)
        #     .with_workdir("/operator")
        #     .directory("test_artifacts_organized")
        # )
        
        # Extract hooks environment variables
        hooks_env_vars = await self._get_hook_env_vars(organized_artifacts)

        # Parse hook environment variables into a dictionary
        hook_env_dict = {}
        if hooks_env_vars.strip():
            for line in hooks_env_vars.strip().split("\n"):
                if line.strip():
                    key, value = line.strip().split("=", 1)
                    hook_env_dict[key] = value

        # Prepare container for running upgrade test
        upgrade_test_container = (
            env
            .with_directory("/test_artifacts_organized", organized_artifacts)
        )
        
        # Add hook environment variables to the container
        for hook_name, hook_path in hook_env_dict.items():
            upgrade_test_container = upgrade_test_container.with_env_variable(hook_name, hook_path)
        
        # Add wekai to the PATH and set environment variables similar to GitHub workflow
        upgrade_test_container = (
            upgrade_test_container
            .with_env_variable("PATH_TO_WEKAI", "/wekai")
            .with_env_variable("DOCS_DIR", "/operator/doc")
            .with_secret_variable("OPENAI_API_KEY", openai_api_key)
            .with_secret_variable("GEMINI_API_KEY", gemini_api_key)
            .with_env_variable("KUBECONFIG", "/.kube/config")
        )

        versions = await env.file("/versions").contents()
        # images.services.scalar:5002/helm/weka-operator:v1.5.1-kristina-test
        operator_helm_image_with_version = versions.split("\n")[1]
        operator_version = operator_helm_image_with_version.split(":")[-1]
        operator_helm_image = operator_helm_image_with_version.removesuffix(f":{operator_version}")
        # images.services.scalar:5002/weka-operator:v1.5.1-kristina-test@sha256:ed47ec60b6d635f3a03022a1f7c82f2db7203c4ba2a7b13ef22545c3dec6b799
        operator_full_image = versions.split("\n")[0]
        operator_image_with_version = operator_full_image.split("@")[0]
        operator_image = operator_image_with_version.removesuffix(f":{operator_version}")
        
        # Execute the upgrade test command
        result = await (
            upgrade_test_container
            .with_exec([
                "/weka-k8s-testing",
                "upgrade-extended",
                "--initial-version", initial_weka_version,
                "--new-version", new_weka_version,
                "--operator-version", operator_version,
                "--operator-image", operator_image,
                "--operator-helm-image", operator_helm_image,
                "--node-selector", "weka.io/dedicated:upgrade-extended",
                "--namespace", "test-upgrade-extended",
                "--cluster-name", "upgrade-extended",
                # "--cleanup"
            ])
            .stdout()
        )
        
        return result

    @function
    async def generate_ai_plan_for_prs(
        self,
        operator: dagger.Directory,
        pr_numbers: List[int],
        gh_token: dagger.Secret,
        openai_api_key: dagger.Secret,
    ) -> dagger.Directory:
        """
        Generate test plans for the specified PR numbers using AI by calling the script_process_pr_hooks script.
        
        Args:
            operator: Dagger Directory of the operator repository
            pr_numbers: List of PR numbers to process
            gh_token: GitHub token to access PR details
            openai_api_key: OpenAI API key for AI model access
            
        Returns:
            Directory containing the generated test artifacts
        """
        from containers.builders import _uv_base
        
        # Create the base container with UV
        base_container = await _uv_base()
        container = (
            base_container
            .with_directory("/operator", operator)
            .with_workdir("/operator")
            .with_env_variable("GITHUB_PAT_TOKEN", await gh_token.plaintext())
            .with_env_variable("OPENAI_API_KEY", await openai_api_key.plaintext())
        )

        # Convert PR numbers to space-separated string
        pr_ids_str = " ".join(str(pr_num) for pr_num in pr_numbers)
        
        # Explicitly run the script with python instead of relying on the shebang
        container = container.with_exec(["uv", "run", "--no-project", "--with", "openai-agents", "python", "workflows/script_process_pr_hooks.py"] + pr_ids_str.split())
        
        # Return the directory with generated artifacts
        return container.directory("/operator/test_artifacts")


    @function
    async def _get_hook_env_vars(self, organized_artifacts: dagger.Directory) -> dagger.File:
        """
        Extract hook environment variables from the organized artifacts.
        
        Args:
            organized_artifacts: Directory containing the organized test artifacts
            
        Returns:
            Dictionary of hook environment variables (hook_name: hook_path)
        """
        # Set up hooks env vars
        hooks_container = (
            dag.container()
            .from_("alpine:latest")
            .with_directory("/test_artifacts_organized", organized_artifacts)
            .with_exec(["sh", "-c", """
                # Find all hook directories starting with 'hook_'
                hook_env_vars=""
                for hook_dir in /test_artifacts_organized/hook_*; do
                    if [ -d "$hook_dir" ]; then
                        # Extract hook name from directory name (remove "hook_" prefix)
                        hook_name=$(basename "$hook_dir" | sed 's/^hook_//')
                        
                        # Check if all_prs_hook.sh exists
                        if [ -f "$hook_dir/all_prs_hook.sh" ]; then
                            echo "Found hook: $hook_name -> $hook_dir/all_prs_hook.sh"
                            hook_env_vars="$hook_env_vars\\n$hook_name=$hook_dir/all_prs_hook.sh"
                            chmod +x "$hook_dir/all_prs_hook.sh"
                        fi
                    fi
                done
                
                # Make all hook scripts executable
                find /test_artifacts_organized -name "*.sh" -exec chmod +x {} \\;
                
                # Save hook environment variables to a file
                echo -e "$hook_env_vars" > /hooks_env_vars.txt
            """]) 
        )
        
        # Get the hooks environment variables
        return await hooks_container.file("/hooks_env_vars.txt").contents()
