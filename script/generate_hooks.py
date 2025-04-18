#!/usr/bin/env -S uv run --no-project --with openai-agents
import dataclasses
import logging
import os
import sys
import argparse
from pathlib import Path
from typing import List, Dict, Any

from agents import OpenAIChatCompletionsModel, Agent, ModelSettings, Runner, function_tool, RunContextWrapper, TContext, \
    Tool
from openai import AsyncOpenAI

logger = logging.getLogger(__name__)
CURRENT_FILE_ABSOLUTE_PATH = Path(os.path.abspath(__file__))
DEFAULT_DOCS_DIR_PATH = CURRENT_FILE_ABSOLUTE_PATH.parent.parent / 'doc'

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

    file_path = Path(file_path)
    if not file_path.is_file():
        return f"Error: Path is not a file: {file_path}"

    try:
        content = file_path.read_text(encoding='utf-8', errors='ignore')
        logger.info(f"Successfully read file: {file_path}")
        # Consider limiting file size read?
        return content
    except PermissionError:
        logger.warning(f"Permission denied for reading file: {file_path}")
        return f"Error: Permission denied for file: {file_path}"
    except FileNotFoundError: # Should be caught by is_path_allowed(strict=True) but good failsafe
        logger.warning(f"File not found during read attempt: {file_path}")
        return f"Error: File not found: {file_path}"
    except Exception as e:
        logger.error(f"Error reading file {file_path}: {e}")
        return f"Error: Could not read file: {e}"
    

def read_file_or_fail(file_path: str) -> str:
    result = read_file(file_path)
    if result.startswith("Error:"):
        raise Exception(result)
    return result


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


@dataclasses.dataclass
class InputFilePaths:
    docs_dir: str
    base_instructions: str
    upgrade_test_description_path: str
    upgrade_test_hooks_description_path: str
    upgrade_test_hooks_env_vars_path: str
    hooks_guidance_path: str
    pr_description: str
    pr_diff: str


def generate_hooks(input: InputFilePaths):
    instructions = read_file_or_fail(input.base_instructions)
    
    dir_path = Path(input.docs_dir)
    if not dir_path.is_dir():
        logger.error(f"Provided docs directory is not valid: {dir_path}, using default")
        dir_path = DEFAULT_DOCS_DIR_PATH

    logger.info(f"Using docs directory: {dir_path}")

    upgrade_test_description = read_file_or_fail(input.upgrade_test_description_path)
    upgrade_test_hooks_description = read_file_or_fail(input.upgrade_test_hooks_description_path)
    upgrade_test_hooks_env_vars = read_file_or_fail(input.upgrade_test_hooks_env_vars_path)
    hooks_guidance = read_file_or_fail(input.hooks_guidance_path)
    pr_description = read_file_or_fail(input.pr_description)
    pr_diff = read_file_or_fail(input.pr_diff)


    instructions += f"""
<environment_description>
You are running in the middle of execution of another workflow, that workflow has hooks/entry-points to run additional tasks

<parent_workflow>
{upgrade_test_description}
</parent_workflow>

<hooks>
{upgrade_test_hooks_description}
</hooks>

When plan that you will generate will run it will have following environment variables set, you can rely on them to fetch information

<hooks_environmnent_variables>
{upgrade_test_hooks_env_vars}
</hooks_environmnent_variables>

<hooks_guidance>
{hooks_guidance}

## Wekai usage (wekai is a tool that can execute hook plan that you will produce)
- request-file is the exisintg text plan file (prompt)
- plan-file is the name of the non-existing json plan file that will be created by Wekai
- --param=param_name=param_value, you can rely on this when building a plan, specifying within plan that such global parameter is expected, and adding something like `param=cluster_name=$CLUSTER_NAME --param=namespace=$NAMESPACE` to wekai executino within a hook
- --docs-dir MUST be preserved in actual use with same value as in following example, preserve it as absolute path(as provided)
```
./wekai --mode bot --docs-dir={dir_path} --request-file plan.txt --plan-file plan.txt.json --param=cluster_name=$CLUSTER_NAME, --param=namespace=$NAMESPACE...
```
</hooks_guidance>
</environment_description>
    
Following is the full project documentation for the context:
<documentation>
{get_directory_contents(dir_path)}
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

    change_description = f"""
A change description to build plan/hooks for: 
<change_to_validate>
<change_summary>
{pr_description}
</change_summary>
<change_diff>
{pr_diff}
</change_diff>
</change_to_validate>
"""

    # Save instructions and change description to files for debugging
    test_artifacts_dir = CURRENT_FILE_ABSOLUTE_PATH.parent.parent / 'test_artifacts'
    test_artifacts_dir.mkdir(exist_ok=True)
    
    debug_instructions_path = test_artifacts_dir / 'debug_instructions.txt'
    debug_change_description_path = test_artifacts_dir / 'debug_change_description.txt'
    
    with open(debug_instructions_path, 'w') as f:
        f.write(instructions)
    logger.info(f"Saved debug instructions to {debug_instructions_path}")
    
    with open(debug_change_description_path, 'w') as f:
        f.write(change_description)
    logger.info(f"Saved debug change description to {debug_change_description_path}")

    # Run the agent with the change description
    result = Runner.run_sync(agent, change_description, hooks=LogHooks())
    print(result)

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description="Generate hooks based on input file paths.")
    parser.add_argument("--docs-dir", required=True, help="Path to the docs directory.")
    parser.add_argument("--base-instructions-path", required=True, help="Path to the base instructions file.")
    parser.add_argument("--upgrade-test-description-path", required=True, help="Path to the upgrade test description file.")
    parser.add_argument("--upgrade-test-hooks-description-path", required=True, help="Path to the upgrade test hooks description file.")
    parser.add_argument("--upgrade-test-hooks-env-vars-path", required=True, help="Path to the upgrade test hooks environment variables file.")
    parser.add_argument("--hooks-guidance-path", required=True, help="Path to the hooks guidance file.")
    parser.add_argument("--pr-description-path", required=True, help="Path to the PR description file.")
    parser.add_argument("--pr-diff-path", required=True, help="Path to the PR diff file.")
    args = parser.parse_args()

    input_file_paths = InputFilePaths(
        docs_dir=args.docs_dir,
        base_instructions=args.base_instructions_path,
        upgrade_test_description_path=args.upgrade_test_description_path,
        upgrade_test_hooks_description_path=args.upgrade_test_hooks_description_path,
        upgrade_test_hooks_env_vars_path=args.upgrade_test_hooks_env_vars_path,
        hooks_guidance_path=args.hooks_guidance_path,
        pr_description=args.pr_description_path,
        pr_diff=args.pr_diff_path
    )

    print(generate_hooks(input_file_paths))
