#!/usr/bin/env -S uv run --no-project --with openai-agents
import dataclasses
import logging
import os
import sys
from pathlib import Path
from typing import List, Dict, Any

from agents import OpenAIChatCompletionsModel, Agent, ModelSettings, Runner, function_tool, RunContextWrapper, TContext, \
    Tool
from openai import AsyncOpenAI

logger = logging.getLogger(__name__)
CURRENT_FILE_ABSOLUTE_PATH = Path(os.path.abspath(__file__))
DIR_PATH = CURRENT_FILE_ABSOLUTE_PATH.parent.parent / 'doc'

# ANTHROPIC_API_KEY = os.getenv("ANTHROPIC_API_KEY")
GEMINI_API_KEY  = os.getenv("GEMINI_API_KEY")

# Set up logging


log_level = logging.DEBUG
logging.basicConfig(
    level=log_level,
    format='%(asctime)s - %(levelname)s - %(message)s',
    handlers=[
        logging.StreamHandler(sys.stderr)
    ]
)

logger = logging.getLogger(__name__)

gemini_client = AsyncOpenAI(base_url="https://generativelanguage.googleapis.com/v1beta/openai/", api_key=GEMINI_API_KEY)

gemini_pro_model = OpenAIChatCompletionsModel(
    model="gemini-2.5-pro-preview-03-25",
    openai_client=gemini_client
)


class LogHooks():
    async def on_agent_start(
            self, context: RunContextWrapper[TContext], agent: Agent[TContext]
    ) -> None:
        """Called before the agent is invoked. Called each time the current agent changes."""
        logger.info(f"starting agent {agent.name} with context {context}")
        pass

    async def on_agent_end(
            self,
            context: RunContextWrapper[TContext],
            agent: Agent[TContext],
            output: Any,
    ) -> None:
        """Called when the agent produces a final output."""
        logger.info(f"ending agent {agent.name} with context {context} and output {output}")

    async def on_handoff(
            self,
            context: RunContextWrapper[TContext],
            from_agent: Agent[TContext],
            to_agent: Agent[TContext],
    ) -> None:
        """Called when a handoff occurs."""
        logger.info(f"handoff from {from_agent.name} to {to_agent.name} with context {context}")

    async def on_tool_start(
            self,
            context: RunContextWrapper[TContext],
            agent: Agent[TContext],
            tool: Tool,
    ) -> None:
        """Called before a tool is invoked."""
        logger.info(f"tool start {tool.name} with context {context}")

    async def on_tool_end(
            self,
            context: RunContextWrapper[TContext],
            agent: Agent[TContext],
            tool: Tool,
            result: str,
    ) -> None:
        """Called after a tool is invoked."""
        logger.info(f"tool end {tool.name} with context {context} and result {result}")

def is_path_allowed(file_path):
    return True


def read_file(file_path: str) -> str:
    """
    Reads the content of a specified absolute file path.
    Access is restricted to pre-approved directories.

    Args:
        file_path: The absolute path of the file to read.

    Returns:
        The content of the file as a string, or an error message string
        if access is denied, the path is not a file, or an error occurs.
    """
    allowed_path = is_path_allowed(file_path)

    if not allowed_path:
        return f"Error: Access denied or invalid path: {file_path}"

    if not allowed_path.is_file():
        return f"Error: Path is not a file: {allowed_path}"

    try:
        content = allowed_path.read_text(encoding='utf-8', errors='ignore')
        logger.info(f"Successfully read file: {allowed_path}")
        # Consider limiting file size read?
        return content
    except PermissionError:
        logger.warning(f"Permission denied for reading file: {allowed_path}")
        return f"Error: Permission denied for file: {allowed_path}"
    except FileNotFoundError: # Should be caught by is_path_allowed(strict=True) but good failsafe
        logger.warning(f"File not found during read attempt: {allowed_path}")
        return f"Error: File not found: {allowed_path}"
    except Exception as e:
        logger.error(f"Error reading file {allowed_path}: {e}")
        return f"Error: Could not read file: {e}"



def read_multiple_files(file_paths: List[str]) -> Dict[str, str]:
    """
    Reads the contents of multiple specified absolute file paths.
    Access is restricted to pre-approved directories for each file.

    Args:
        file_paths: A list of absolute file paths to read.

    Returns:
        A dictionary where keys are the original file paths and values are
        either the file content (string) or an error message (string) for that specific file.
    """
    logger.info(f"Tool 'read_multiple_files' called with paths: {file_paths}")
    results: Dict[str, str] = {}
    for file_path in file_paths:
        # Use the existing read_file logic for each path
        content_or_error = read_file(file_path)
        results[file_path] = content_or_error
        # Logging is handled within read_file

    logger.info(f"Finished processing multiple files. Results count: {len(results)}")
    return results


def write_file(file_path: str, content: str) -> str:
    """
    Writes content to a specified absolute file path.
    If directories in the path do not exist, they will be created automatically.
    Access is restricted to pre-approved directories.

    Args:
        file_path: The absolute path of the file to write.
        content: The content to write to the file.

    Returns:
        A success message string, or an error message string if access is denied or an error occurs.
    """
    allowed_path = is_path_allowed(file_path)

    if not allowed_path:
        return f"Error: Access denied or invalid path: {file_path}"

    try:
        # Create parent directories if they don't exist
        parent_dir = Path(file_path).parent
        parent_dir.mkdir(parents=True, exist_ok=True)

        # Write content to file
        Path(file_path).write_text(content, encoding='utf-8')
        logger.info(f"Successfully wrote to file: {file_path}")
        return f"Successfully wrote to file: {file_path}"
    except PermissionError:
        logger.warning(f"Permission denied for writing to file: {file_path}")
        return f"Error: Permission denied for file: {file_path}"
    except Exception as e:
        logger.error(f"Error writing to file {file_path}: {e}")
        return f"Error: Could not write to file: {e}"


import dataclasses
from typing import List, Dict # Added List for type hinting

# Assuming FileParams and FileWriteResult are defined as shown in the context
@dataclasses.dataclass
class FileParams:
    path: str
    content: str

@dataclasses.dataclass
class FileWriteResult:
    path: str
    success: bool
    error: str

def write_multiple_files(files: List[FileParams]) -> List[FileWriteResult]:
    """
    Writes content for multiple files specified in the input list.
    If directories in the paths do not exist, they will be created automatically by write_file.
    Access is restricted to pre-approved directories for each file (handled by write_file).

    Args:
        files: A list of FileParams objects, each containing the absolute file path,
               and content

    Returns:
        A list of FileWriteResult objects, each indicating the success or failure
        for the corresponding input file.
    """
    logger.info(f"Tool 'write_multiple_files' called with {len(files)} files.")
    results: List[FileWriteResult] = []
    for file_param in files:
        # Use the existing write_file logic for each path
        # Assume write_file returns a message starting with "Error:" on failure
        result_message = write_file(file_param.path, file_param.content) # Ignoring mode for now as write_file signature doesn't use it

        is_success = not result_message.startswith("Error:")
        error_msg = "" if is_success else result_message

        write_result = FileWriteResult(
            path=file_param.path,
            success=is_success,
            error=error_msg
        )
        results.append(write_result)
        # Logging for individual files is assumed to be handled within write_file

    logger.info(f"Finished processing multiple files. Results count: {len(results)}")
    return results



def get_directory_contents(dir_path: Path) -> str:
    """
    Recursively finds all files within the specified directory, reads their content,
    and returns a string with each file's content wrapped in XML-like tags
    including the relative path.

    Args:
        dir_path: The path to the directory.

    Returns:
        A string containing the formatted content of all files,
        or an error message if the path is invalid or not a directory.
    """
    try:
        base_path = Path(dir_path)
        if not base_path.is_dir():
            logger.warning(f"Path is not a valid directory: {dir_path}")
            return f"Error: Path is not a valid directory: {dir_path}"

        all_files_content = []
        for item_path in base_path.rglob("*"):
            if item_path.is_file():
                bad_file = False
                for part in ['.gitkeep', '.gitignore', '.DS_Store']:
                    if part in str(item_path):
                        bad_file = True
                        break
                if bad_file:
                    continue
                try:
                    relative_path = item_path.relative_to(base_path)
                    content = item_path.read_text(encoding='utf-8', errors='replace')
                    formatted_content = f'<file path="{relative_path}">\n{content}\n</file>'
                    all_files_content.append(formatted_content)
                except Exception as read_error:
                    logger.warning(f"Could not read file {item_path}: {read_error}")
                    # Optionally include a note about the unreadable file in the output
                    all_files_content.append(f'<file path="{item_path.relative_to(base_path)}" error="Could not read file: {read_error}"/>')


        if not all_files_content:
            logger.info(f"No readable files found in directory: {dir_path}")
            return f"No readable files found in directory: {dir_path}"

        return "\n".join(all_files_content)

    except Exception as e:
        logger.error(f"Error processing directory {dir_path}: {e}", exc_info=True)
        return f"Error processing directory {dir_path}: {e}"


def generate_hooks():
    instructions = """
    # Your Task
Your goal is to generate plan that will write hook files and txt plans,  that will be executed later. It is not your direct goal to execute

Given the provided context and the details below, make the decision if you are able to test the changes introduced in the PR using the existing upgrade test hooks
(while upgrade test is running) and generate the necessary files for that.

This test is executed on top of existing environment in the middle of other test, details will follow in form of `<context>`, `<environment_details>` and so on

If YES:
Generate the test-in-the-hook plan text file assuming that you dessribe the steps running in the middle of existing test.
   - Rely on available environment variable specified in test description
   - Use the plain text format, describing steps to test new functionality
   - test-in-the-hook plan text should assume and specify that it is running in pre-existing environment, any changes to environment should be reverted in the end of plan
   - Create a hook script that contains ./wekai call with the generated text plan as the request file.
    - You must use ./wekai call in hook, wekai will be executing plan.txt file
   - Both the test plan and the hook script should be placed in the ./test_artifacts
   - For each hook to be used place them under `./test_artifacts/hook_HOOK_NAME/hook.sh and `./test_artifacts/hook_HOOK_NAME/plan.txt`
   - You are allowed to break up validation into multiple hooks, each one getting required instructions, they will not be able to exchange information and each such plan should be treat stand-alone. But you can rely on multiple hooks being executed, each should have a test plan and a bash hook entry point
If NO:
    - Explain why hooks cannot be used (e.g., no appropriate hook points, missing docs)
    - Generate a standalone test plan that will be used instead of running upgrade test
  or explain why this PR cannot/shouldn't be tested.
    - Place the test plan in the ./test_artifacts directory with the name test_plan.txt
<additional_instructions>
- you must write hook files when applicable, using provided write_multiple_files tool
- you do not have access to any repository files, you're working only with weka CRs and k8s resources
- If the PR is only changing documentation, comments, or making cosmetic changes, it doesn't make sense to run the upgrade test at all.
- Your goal is to determine the most efficient way to test the changes in this PR, if testing is needed at all.
- you do not have access to repository files, so any file that was added/removed in change description is NOT available to you
- you do not have access to github tools, the only entry point available for you are described hooks, they run in isolated environment with access to bash,kubectl, but not project files/github
- generated test plan should not mention your instructions/scenario, it should only contain what needs to be done, omitting general context of why this being done
- in generated test plan break down each hook handling to separate step, writing required files into target location
- never rely on project files to validate new functionality, files diff provided only for context of what functionality has changed
    - only rely on states of system being tested and hooks of this validation framework
</additional_instructions>


<environment_description>
You are running in the middle of execution of another workflow, that workflow has hooks/entry-points to run additional tasks

<parent_workflow>
Test goal:
    - validate that cluster can be provisioned and upgraded to newer version
Test scenario
    - provision a wekacluster CR, 7 compute and 7 drive containers, 6 drives per container, 3 stripe width, 2 redundancy level, 1 hot spare
        - set toleration for all effects of weka.io/upgrade taint, use rawTolerations field which is in the form of a list of k8s toleration objects
        - set overrides.upgradeForceReplaceDrives=true on wekacluster CR
        - set overrides.upgradePausePreCompute=true on wekacluster CR
    - provision clients
        - set toleration for all effects of weka.io/upgrade taint, use rawTolerations field which is in the form of a list of k8s toleration objects
    - provision csi and workload
    - upgrade cluster
    - once first drive container was replaced (wekacontainer's status.lastAppliedImage becomes equal to target) - add 1 more computeContainers to the cluster, i.e set higher .spec.dynamicTemplate.computeContainers
        - validate that new compute pod was created and are running, no need to validate connectivity to weka, even more - it is expected of them not to join weka cluster at this stage
    - wait for all drives containers to rotate to new version, by watching wekacontainer's status.lastAppliedImage to become equal to target version
    - wait one minute doing nothing
    - validate that aside of one new compute containers there is no other compute containers with .spec.image equal to target
    - check that we remain in drive phase by checking "weka status --json | grep upgrade_phase"
    - change wekacluster .spec.upgradePausePreCompute to false
    - wait for compute phase to start, by checking "weka status --json | grep upgrade_phase"
    - wait for all pre-existing computes to rotate
    - check for status of new computes, by looking up their in-weka container name (.spec.name of wekacontainer)
        - lookup this name within weka, by running "weka cluster container | grep <name>"
        - do not abort regardless of result, just capture what was a result, existing on weka cluster side or not
    - set taints, upgrade clients
    - validate that all backend containers (drive, compute) are now running with lastAppliedImage equal to target
    - validate that all containers can be found in weka cluster and have status UP by running "weka cluster container | grep <name> | grep UP"
Testing notes:
- this tests run on physical environment, use 10.200.0.0/16 subnet for testing
- use weka.io/upgrade taint/toleration to have a mechanism to evict load but not evict wekacluster/wekaclient pods
- allowed to use tolerations effects: NoSchedule, NoExecute, PreferNoSchedule
- ensure that user provided newVersion and oldVersion parameters and use them
Required user parameters to validate and use:
 - nodeSelector, to use for provisioning all resources, no nodes should be touched outside of this nodeselector and no resources should be provisioned that are not using this nodeselector
 - initialVersion, to provision cluster with
 - newVersion, to upgrade wekacluster and wekaclient to this version
 - namespace to use for testing, if namespace is not provided - autogenerate one with test- prefix, even if user provided it might not exist and you should create it
</parent_workflow>
<hooks>
Hooks allow you to run custom scripts at predefined points in the upgrade flow.
This enables additional validations, customizations, or data collection during tests.
## Available Hook Points
The following hooks are available for the upgrade-extended test:
1. **PRE_SETUP_HOOK**
   - Runs before any setup begins
   - Example: Validate pre-conditions or prepare test environment
2. **POST_SETUP_HOOK**
   - Runs after the environment is set up (cluster, client, CSI, workload)
   - Example: Verify initial setup or collect baseline metrics
3. **PRE_UPGRADE_HOOK**
   - Runs just before starting the upgrade process
   - Example: Perform additional validation before upgrade or backup data
4. **POST_DRIVE_UPGRADE_HOOK**
   - Runs after all drive containers are upgraded
   - Example: Verify drive health or check cluster status
5. **PRE_COMPUTE_UPGRADE_HOOK**
   - Runs before starting compute upgrade phase
   - Example: Check system readiness before proceeding to compute upgrade
6. **POST_COMPUTE_UPGRADE_HOOK**
   - Runs after all compute containers are upgraded
   - Example: Verify compute health or perform computation tests
7. **PRE_CLIENT_UPGRADE_HOOK**
   - Runs before client upgrade begins
   - Example: Check client status or prepare for client upgrade
8. **POST_CLIENT_UPGRADE_HOOK**
   - Runs after client upgrade completes
   - Example: Verify client functionality or run client tests
9. **POST_TEST_HOOK**
   - Runs after the test completes
   - Example: Collect final metrics or generate reports
10. **PRE_CLEANUP_HOOK**
    - Runs before cleanup begins
    - Example: Save logs or perform custom cleanup operations
</hooks>

When plan that you will generate will run it will have following environment variables set, you can rely on them to fetch information

<hooks_environmnent_variables>
## Environment Variables
The following environment variables are automatically passed to all hook scripts:
| Variable | Description |
|----------|-------------|
| `CLUSTER_NAME` | Name of the Weka cluster |
| `CLUSTER_NAMESPACE` | Kubernetes namespace where resources are deployed |
| `CLIENT_NAME` | Name of the Weka client |
| `INITIAL_VERSION` | Initial version of the Weka software |
| `NEW_VERSION` | Target version for the upgrade |
| `KUBECONFIG` | Path to kubeconfig file |
| `OPERATOR_VERSION` | Version of the Weka operator |
</hooks_environmnent_variables>

<hooks_guidance>
1. Make scripts executable (`chmod +x your_script.sh`)
2. Use the shebang line (`#!/bin/bash`)
3. Set `set -e` to fail on errors
4. Use the provided environment variables
5. Return non-zero exit code to fail the test if validation fails
6. Log clearly what the script is doing
## Example Hook Script

```bash
#!/bin/bash
set -e
echo "==== Running verification hook ===="
echo "Cluster name: $CLUSTER_NAME"
echo "Namespace: $CLUSTER_NAMESPACE"
# Execute verifications using kubectl
kubectl get wekacluster $CLUSTER_NAME -n $CLUSTER_NAMESPACE -o json | \
  jq '.status.phase' | \
  grep -q "Ready" || exit 1
# Check for specific conditions
kubectl get pods -n $CLUSTER_NAMESPACE | grep -q "container-name" || {
  echo "Error: Expected pod not found"
  exit 1
}
echo "==== Verification completed successfully ===="
exit 0
```

## Wekai usage (wekai is a tool that can execute hook plan that you will produce)
- request-file is the exisintg text plan file (prompt)
- plan-file is the name of the non-existing json plan file that will be created by Wekai
- --param=param_name=param_value, you can rely on this when building a plan, specifying within plan that such global parameter is expected, and adding something like `param=cluster_name=$CLUSTER_NAME --param=namespace=$NAMESPACE` to wekai executino within a hook
- --docs-dir MUST be preserved in actual use with same value as in following example, preserve it as absolute path(as provided)
```
./wekai --mode bot --docs-dir=%s --request-file plan.txt --plan-file plan.txt.json --param=cluster_name=$CLUSTER_NAME, --param=namespace=$NAMESPACE...
```
</hooks_guidance>
    """ % DIR_PATH

    instructions += f"""
Following is the full project documentation for the context:
<documentation>
{get_directory_contents(DIR_PATH)}
</documentation>

You will be prompted with change description and should follow instructions based on it
Pay special attention to section marked with "How to test"
Pay special attention to AITEST markers, as they are explicitly purposed as YOUR instructions
    """
    agent = Agent(
        name="hook_generator",
        model="o4-mini",
        # model=gemini_pro_model,
        instructions=instructions,
        model_settings=ModelSettings(
            max_tokens=64000,
            # reasoning=dict(
            # effort="high",
            # ),
        ),
        tools = [function_tool(write_multiple_files)],
    )

    input = """
    A change description to build plan/hooks for: 
    <change_to_validate>
    ### TL;DR
    Added a new GitHub workflow for running tests with AI hooks and fixed the existing build workflow.
    ### What changed?
    - Added a new workflow file `.github/workflows/pr_ai_test.yaml` that:
      - Runs on pull requests
      - Uses AI to analyze PR changes and determine if they can be tested with hooks
      - Integrates with a private `wekai` tool to generate test plans
      - Includes steps to fetch PR information, analyze diffs, and generate appropriate test artifacts
    - Updated the `.github/workflows/build.yaml` workflow:
      - Removed unnecessary `permissions` block at the top level
      - Removed `fetch-depth: 0` from checkout step
      - Replaced Go module caching with the built-in caching in actions/setup-go@v5
      - Updated Go setup to use version from go.mod instead of hardcoded version
    - Added a newline at the end of `aitests/upgrade_test_extended.txt`
    ### How to test?
    - Validate each hook to be executed
    - Validate cluster/clients state as expected to be within that hook state, if relevant
    - AITEST: MUST generate each hook (hook.sh+plan.txt), DISREGARD the fact that this is github workflow/CI changes. It still can be tested by producing hooks for validating that hooks functionality is working as expected.
    ### Why make this change?
    This change improves the CI/CD pipeline by adding AI-powered test generation capabilities. The new workflow can automatically analyze PR changes and determine the appropriate testing strategy, making the testing process more efficient. The updates to the build workflow leverage newer GitHub Actions features and simplify the configuration.
    ================ END OF PR DESCRIPTION ================
    ### PR Diff (Summary)
    This diff was too large to include in full. Here is a summary:
    **Files changed:** 3 total files
    **Lines changed:** +370, -18
    **Changed files (showing first 20):**
    - .github/workflows/build.yaml
    - .github/workflows/pr_ai_test.yaml
    - aitests/upgrade_test_extended.txt
    **Preview of the beginning of the diff:**
    ```diff
    diff --git a/.github/workflows/build.yaml b/.github/workflows/build.yaml
    index 25e0bcdcd..5007ea04c 100644
    --- a/.github/workflows/build.yaml
    +++ b/.github/workflows/build.yaml
    @@ -2,8 +2,7 @@
     name: Build
     on:
       push:
    -permissions:
    -  contents: write
    +
     jobs:
       optimize_ci:
         runs-on: ubuntu-latest
    @@ -25,8 +24,6 @@ jobs:
         steps:
           - name: Checkout
             uses: actions/checkout@v3
    -        with:
    -          fetch-depth: 0
           - name: Set up SSH # See: https://github.com/webfactory/ssh-agent?tab=readme-ov-file#support-for-github-deploy-keys
             uses: webfactory/ssh-agent@v0.9.0
             with:
    @@ -39,27 +36,17 @@ jobs:
               git config --global url."ssh://git@github.com/".insteadOf https://github.com/
           - name: Install submodules
             run: git submodule update --init --recursive
    -      - name: Cache Go modules
    -        uses: actions/cache@v3
    -        with:
    -          path: |
    -            ~/.cache/go-build
    -            ~/.local/share/go/pkg/mod
    -     ...
    ```
    </change_to_validate>
    """

    result = Runner.run_sync(agent, input, hooks=LogHooks())
    print(result)

if __name__ == '__main__':
    print(generate_hooks())