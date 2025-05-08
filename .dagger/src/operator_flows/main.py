import re
import logging
from typing import Dict, List, Annotated, Optional

import dagger
from dagger import dag, function, object_type, Ignore

from containers.builders import build_go
from utils.github import GitHubClient

OPERATOR_EXCLUDE_LIST = [
    "node_modules",
    ".aider*",
    "*/.git",
    ".dagger",
    "bin",
    "build",
    "terraform",
]


logger = logging.getLogger(__name__)
logger.setLevel(logging.INFO)
logger.addHandler(logging.StreamHandler())

@object_type
class OperatorFlows:
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
    async def build_scalar(self,
                           operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
                           sock: dagger.Socket,
                           ) -> str:
        from apps.operator import publish_operator, publish_operator_helm_chart
        _ = await publish_operator(operator, sock,
                                   repository="images.scalar.dev.weka.io:5002/weka-operator",
                                   )
        operator_helm = await publish_operator_helm_chart(operator, sock,
                                                          repository="images.scalar.dev.weka.io:5002/helm",
                                                          )
        return operator_helm

    @function
    async def deploy_scalar(self,
                            operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
                            sock: dagger.Socket,
                            kubeconfig: dagger.Secret,
                            operator_values: Optional[dagger.File]=None,
                            ) -> str:
        from apps.operator import install_helm_chart
        operator_helm = await self.build_scalar(operator, sock)
        install = await install_helm_chart(
            image=operator_helm,
            kubeconfig=kubeconfig,
            operator_repo="images.scalar.dev.weka.io:5002/weka-operator",
            values_file=operator_values,
        )
        return install

    @function
    async def ci_on_merge_queue_env(self,
                                    operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
                                    testing: Annotated[dagger.Directory, Ignore([
                                        ".aider*",
                                        "*/.git",
                                    ])],
                                    wekai: Annotated[dagger.Directory, Ignore([
                                        ".aider*",
                                        "*/.git",
                                        ".dagger",
                                    ])],
                                    sock: dagger.Socket,
                                    gh_token: Optional[dagger.Secret] = None,
                                    ) -> dagger.Container:
        from containers.builders import _uv_base
        from apps.operator import publish_operator, publish_operator_helm_chart

        wekai = await build_go(wekai, sock, gh_token)
        testing = await build_go(testing, sock, cache_deps=False, gh_token=gh_token)
        operator_image = await publish_operator(operator, sock,
                                                repository="images.scalar.dev.weka.io:5002/weka-operator",
                                                gh_token=gh_token,
                                                )
        operator_helm = await publish_operator_helm_chart(operator, sock,
                                                          repository="images.scalar.dev.weka.io:5002/helm",
                                                          gh_token=gh_token,
                                                          )

        base_container = await _uv_base()
        base_container = (
            base_container
            .with_file("/wekai", wekai.file("/out-binary"))
            .with_file("/weka-k8s-testing", testing.file("/out-binary"))
            .with_new_file("/versions", f"{operator_image}\n{operator_helm}")
        )

        return base_container

    def _extract_pr_numbers(self, title: str) -> List[int]:
        """Extracts PR numbers from a title string containing format like '(PRs 1132, 1133,...)'"""
        match = re.search(r'\(PRs\s+([\d,\s]+)\)', title)
        if match:
            numbers_str = match.group(1)
            return [int(num.strip()) for num in numbers_str.split(',')]
        return []

    @function
    async def generate_pr_test_artifacts(
        self,
        operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
        pr_number: int,
        gh_token: dagger.Secret,
        openai_api_key: dagger.Secret,
        export_path: Optional[str] = None,
    ) -> dagger.Directory:
        """Generates test artifacts for a given Pull Request for the merge queue CI."""
        logger.info(f"Starting test artifact generation for PR #{pr_number}")

        logger.info("Creating GitHub client for artifact generation")
        gh_client = GitHubClient("weka/weka-operator", await gh_token.plaintext())
        pr = gh_client.get_pr_details(pr_number)
        if pr is None:
            raise ValueError(f"Could not retrieve details for PR #{pr_number}")

        if "[Graphite MQ]" in pr.title:
            pr_numbers = self._extract_pr_numbers(pr.title)
        else:
            pr_numbers = [pr_number]

        logger.info(f"Processing PRs {pr_numbers} for artifact generation")

        test_artifacts = await self.generate_ai_plan_for_prs(
            operator=operator,
            pr_numbers=pr_numbers,
            gh_token=gh_token,
            openai_api_key=openai_api_key,
        )
        logger.info(f"Successfully generated test artifacts for PRs {pr_numbers}")

        if export_path:
            await test_artifacts.export(export_path)
            logger.info(f"Exported test artifacts to {export_path}")

        return test_artifacts

    @function
    async def ci_on_merge_queue_plan(
        self,
        operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
        testing: Annotated[dagger.Directory, Ignore([
            ".aider*",
            "*/.git",
        ])],
        wekai: Annotated[dagger.Directory, Ignore([
            ".aider*",
            "*/.git",
            ".dagger",
        ])],
        sock: dagger.Socket,
        pr_number: int,
        gh_token: dagger.Secret,
        openai_api_key: dagger.Secret,
        gemini_api_key: dagger.Secret,
        kubeconfig_path: dagger.Secret,
        initial_weka_version: str = "quay.io/weka.io/weka-in-container:4.4.5.95-k8s-safe-stop-and-metrics-alpha",
        new_weka_version: str = "quay.io/weka.io/weka-in-container:4.4.5.118-k8s.3",
        test_artifacts_dir: Optional[dagger.Directory] = None,
        dry_run: bool = False,
        use_gh_token_for_go_deps: bool = False,
    ) -> dagger.Directory:
        """Executes the merge queue plan using pre-generated test artifacts (if provided) or generates them."""

        current_gh_token = None
        if use_gh_token_for_go_deps:
            if gh_token is None:
                logger.error("gh_token must be provided if use_gh_token_for_go_deps is True for env setup")
                raise ValueError("gh_token must be provided if use_gh_token_for_go_deps is True for env setup")
            current_gh_token = gh_token

        if not test_artifacts_dir:
            test_artifacts_dir = await self.generate_pr_test_artifacts(operator, pr_number, gh_token, openai_api_key)
    
        logger.info("Extracting hook environment variables from provided artifacts.")
        hook_env_dict = await self._get_hook_env_vars(test_artifacts_dir)
        logger.info(f"Hook env vars: {hook_env_dict}")

        if not hook_env_dict:
            logger.info("No generated hooks found, skipping upgrade test")
            return test_artifacts_dir
        
        env = await self.ci_on_merge_queue_env(operator, testing, wekai, sock, current_gh_token)

        env = (
            env
            .with_exec(["mkdir", "-p", "/.kube"])
            .with_mounted_secret("/.kube/config", kubeconfig_path)
            # install curl
            .with_exec(["sh", "-c", "apk add curl"])
            # install kubectl
            .with_exec(["sh", "-c", "curl -LO https://dl.k8s.io/release/v1.29.0/bin/linux/amd64/kubectl && chmod +x kubectl && mv kubectl /usr/local/bin/"])
        )

        # Prepare container for running upgrade test
        upgrade_test_container = (
            env
            .with_directory("/doc", operator.directory("doc"))
            .with_directory("/test_artifacts", test_artifacts_dir)
            # Make hook scripts executable
            .with_exec(["sh", "-c", "find /test_artifacts/ -name '*.sh' -exec chmod +x {} \\;"])
        )

        # Add hook environment variables to the container
        for hook_name, hook_path in hook_env_dict.items():
            upgrade_test_container = upgrade_test_container.with_env_variable(hook_name, hook_path)

        # Add wekai to the PATH and set environment variables similar to GitHub workflow
        upgrade_test_container = (
            upgrade_test_container
            .with_env_variable("PATH_TO_WEKAI", "/wekai")
            .with_env_variable("DOCS_DIR", "/doc")
            .with_secret_variable("OPENAI_API_KEY", openai_api_key)
            .with_secret_variable("GEMINI_API_KEY", gemini_api_key)
            .with_env_variable("KUBECONFIG", "/.kube/config")
        )

        versions = await env.file("/versions").contents()
        # images.scalar.dev.weka.io:5002/helm/weka-operator:v1.5.1-kristina-test
        operator_helm_image_with_version = versions.split("\n")[1]
        operator_version = operator_helm_image_with_version.split(":")[-1]
        operator_helm_image = operator_helm_image_with_version.removesuffix(f":{operator_version}")
        # images.scalar.dev.weka.io:5002/weka-operator:v1.5.1-kristina-test@sha256:ed47ec60b6d635f3a03022a1f7c82f2db7203c4ba2a7b13ef22545c3dec6b799
        operator_full_image = versions.split("\n")[0]
        operator_image_with_version = operator_full_image.split("@")[0]
        operator_image = operator_image_with_version.removesuffix(f":{operator_version}")

        if not dry_run:
            logger.info("Executing upgrade test.")
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
                    # "--cleanup", "no-cleanup"
                ])
            )
            # Return a directory that has both the test artifacts and result
            return (
                dag.directory()
                .with_directory("test_artifacts", test_artifacts_dir)
                .with_directory("test_result", result.directory("/"))
            )
        else:
            # In dry run mode, just return the artifacts directory
            return test_artifacts_dir


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
            .with_secret_variable("GITHUB_PAT_TOKEN", gh_token)
            .with_secret_variable("OPENAI_API_KEY", openai_api_key)
        )

        # Convert PR numbers to space-separated string
        pr_ids_str = " ".join(str(pr_num) for pr_num in pr_numbers)

        # Explicitly run the script with python instead of relying on the shebang
        container = container.with_exec(["uv", "run", "--no-project", "--with", "openai-agents", "python",
                                         "workflows/script_process_pr_hooks.py"] + pr_ids_str.split())

        # Return the directory with generated artifacts
        return container.directory("/operator/test_artifacts")

    async def _get_hook_env_vars(self, test_artifacts: dagger.Directory) -> Dict[str, str]:
        """
        Extract hook environment variables from the test artifacts.
        
        Args:
            test_artifacts: Directory containing the test artifacts
            
        Returns:
            Dictionary of hook environment variables (hook_name: hook_path)
        """
        # Set up hooks env vars
        hooks_container = (
            dag.container()
            .from_("alpine:latest")
            .with_directory("/test_artifacts", test_artifacts)
            .with_exec(["tree", "/test_artifacts"])
            .with_exec(["sh", "-c", """
                # Find all hook directories starting with 'hook_'
                hook_env_vars=""
                for hook_dir in /test_artifacts/hooks/hook_*; do
                    if [ -d "$hook_dir" ]; then
                        # Extract hook name from directory name (remove "hook_" prefix)
                        hook_name=$(basename "$hook_dir" | sed 's/^hook_//')
                        
                        # Check if hook.sh exists
                        if [ -f "$hook_dir/hook.sh" ]; then
                            echo "Found hook: $hook_name -> $hook_dir/hook.sh"
                            hook_env_vars="$hook_env_vars\\n$hook_name=$hook_dir/hook.sh"
                        fi
                    fi
                done
                        
                # Save hook environment variables to a file
                echo -e "$hook_env_vars" > /hooks_env_vars.txt
            """])
        )

        # Get the hooks environment variables
        hooks_env_vars = await hooks_container.file("/hooks_env_vars.txt").contents()

        # Parse hook environment variables into a dictionary
        hook_env_dict = {}
        if hooks_env_vars.strip():
            for line in hooks_env_vars.strip().split("\n"):
                if line.strip():
                    key, value = line.strip().split("=", 1)
                    hook_env_dict[key] = value
        return hook_env_dict

    @function
    async def operator_explore(self,
                               operator: Annotated[dagger.Directory, Ignore(OPERATOR_EXCLUDE_LIST)],
                               ) -> dagger.Container:
        return await (
            dag.container()
            .from_("ubuntu:24.04")
            .with_directory("/operator", operator)
        )
