#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import asyncio
import logging
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, Any, Optional

from github import GitHubClient, GitHubError
from generate_hooks import generate_hooks, InputFilePaths

# Set up logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(levelname)s - %(message)s',
    handlers=[
        logging.StreamHandler(sys.stderr)
    ]
)
logger = logging.getLogger(__name__)

# Constants
DEFAULT_REPO = "weka/weka-operator"
CURRENT_DIR = Path(os.path.dirname(os.path.abspath(__file__)))
PROJECT_ROOT = CURRENT_DIR.parent
DOCS_DIR = PROJECT_ROOT / "doc"
AITESTS_DIR = PROJECT_ROOT / "aitests"
ARTIFACTS_BASE_DIR = PROJECT_ROOT / "test_artifacts"


@dataclass
class PRInfo:
    """Class to store information about a processed PR"""
    number: int
    title: str
    description: Optional[str]
    diff: Optional[str]
    diff_summary: Optional[Dict[str, Any]] = None
    output_dir: Optional[Path] = None


def create_diff_summary(diff_content: str) -> Dict[str, Any]:
    """
    Create a summary of a large diff to prevent huge diffs in prompts.
    
    Args:
        diff_content: Raw diff content
        
    Returns:
        A dictionary with summary information
    """
    # Define max size for raw diff
    MAX_DIFF_SIZE = 10 * 1024  # 10 KB limit for raw diff
    
    if len(diff_content) <= MAX_DIFF_SIZE:
        return {
            "truncated": False,
            "content": diff_content
        }
    
    # Get the number of changed files from the diff
    changed_files_count = len(re.findall(r"^diff --git", diff_content, re.MULTILINE))
    
    # Extract file names that were changed
    file_regex = r"^diff --git a/(.*?) b/(.*?)$"
    changed_files = [match.group(2) for match in re.finditer(file_regex, diff_content, re.MULTILINE)]
    
    # Get some stats on lines changed
    addition_lines = len(re.findall(r"^\+[^+]", diff_content, re.MULTILINE))
    deletion_lines = len(re.findall(r"^-[^-]", diff_content, re.MULTILINE))
    
    # Create a summary
    return {
        "truncated": True,
        "changedFilesCount": changed_files_count,
        "changedFiles": changed_files[:20],  # Limit to first 20 files
        "moreFiles": len(changed_files) > 20,
        "additionLines": addition_lines,
        "deletionLines": deletion_lines,
        "diffPreview": diff_content[:1000] + "..."  # Short preview of the beginning
    }


def generate_diff_section(diff_content: str, diff_summary: Optional[Dict[str, Any]] = None) -> str:
    """
    Generate a readable diff section for the PR based on raw diff or summary.
    
    Args:
        diff_content: Raw diff content
        diff_summary: Optional pre-computed diff summary
        
    Returns:
        A formatted string with the diff information
    """
    if not diff_summary:
        diff_summary = create_diff_summary(diff_content)
    
    if diff_summary.get("truncated", False):
        # Create a human-readable summary
        files_count = diff_summary.get("changedFilesCount", 0)
        changed_files = diff_summary.get("changedFiles", [])
        more_files = diff_summary.get("moreFiles", False)
        additions = diff_summary.get("additionLines", 0)
        deletions = diff_summary.get("deletionLines", 0)
        diff_preview = diff_summary.get("diffPreview", "")
        
        return f"""### PR Diff (Summary)
This diff was too large to include in full. Here is a summary:

**Files changed:** {files_count} total files
**Lines changed:** +{additions}, -{deletions}

**Changed files (showing first {len(changed_files)}):**
- {os.linesep.join(['- ' + f for f in changed_files])}
{("- ... and more files not shown" if more_files else "")}

**Preview of the beginning of the diff:**
```diff
{diff_preview}
```"""
    else:
        # Use the original diff content
        return f"""### PR Diff
```diff
{diff_content}
```"""


def setup_output_directory(pr_number: int) -> Path:
    """
    Create an output directory for a specific PR.
    
    Args:
        pr_number: PR number
        
    Returns:
        Path to the created directory
    """
    output_dir = ARTIFACTS_BASE_DIR / f"pr_{pr_number}"
    output_dir.mkdir(parents=True, exist_ok=True)
    return output_dir


def get_input_files(pr_info: PRInfo) -> InputFilePaths:
    """
    Create necessary input files and return paths for hook generation.
    Directly references the original source locations.
    
    Args:
        pr_info: PRInfo object with PR details
        
    Returns:
        InputFilePaths object containing all required file paths
    """
    output_dir = pr_info.output_dir
    
    # Create diff section
    diff_section = generate_diff_section(pr_info.diff, pr_info.diff_summary)
    
    # Save PR description
    pr_desc_path = output_dir / "pr_description.txt"
    with open(pr_desc_path, 'w') as f:
        f.write(f"#{pr_info.number} {pr_info.title}\n")
        f.write(pr_info.description or "")
    
    # Save PR diff
    pr_diff_path = output_dir / "pr_diff.txt"
    with open(pr_diff_path, 'w') as f:
        f.write(diff_section)
    
    # Define source files with their original paths
    source_files = {
        # Files from aitests/upgrade_extended/
        'base_instructions': AITESTS_DIR / 'upgrade_extended' / 'base_instructions.txt',
        'hooks_guidance': AITESTS_DIR / 'upgrade_extended' / 'hooks_guidance.txt',
        'upgrade_test_description': AITESTS_DIR / 'upgrade_extended' / 'test_description.md',
        'upgrade_test_hooks_description': AITESTS_DIR / 'upgrade_extended' / 'hooks.md',
        'upgrade_test_hooks_env_vars': AITESTS_DIR / 'upgrade_extended' / 'hooks_environmnent_variables.md',
    }
    
    # Check all source files exist
    for _, src_path in source_files.items():
        if not src_path.exists():
            logger.error(f"Required file {src_path} not found")
            raise FileNotFoundError(f"Required file {src_path} not found")
    
    # Create InputFilePaths object with original file paths
    return InputFilePaths(
        docs_dir=str(DOCS_DIR),
        base_instructions=str(source_files['base_instructions']),
        upgrade_test_description_path=str(source_files['upgrade_test_description']),
        upgrade_test_hooks_description_path=str(source_files['upgrade_test_hooks_description']),
        upgrade_test_hooks_env_vars_path=str(source_files['upgrade_test_hooks_env_vars']),
        hooks_guidance_path=str(source_files['hooks_guidance']),
        pr_description=str(pr_desc_path),
        pr_diff=str(pr_diff_path)
    )


async def run_generate_hooks(pr_info: PRInfo, input_paths: InputFilePaths, dry_run: bool = False) -> None:
    """
    Run the generate_hooks function to process the PR information.
    
    Args:
        pr_info: PRInfo object with PR details
        input_paths: InputFilePaths object with paths to use as input
        dry_run: If True, just print what would happen without executing
    """
    if dry_run:
        logger.info("[DRY RUN] Would generate hooks with the following paths:")
        for key, path in input_paths.__dict__.items():
            logger.info(f"  {key}: {path}")
        logger.info(f"  Output directory: {pr_info.output_dir}")
        return
    
    try:
        # Call generate_hooks function directly with InputFilePaths object
        logger.info(f"Generating hooks using InputFilePaths object")
        await generate_hooks(input_paths=input_paths, output_dir=pr_info.output_dir)
        logger.info(f"Successfully generated hooks in {pr_info.output_dir}")
    except Exception as e:
        logger.error(f"Failed to generate hooks: {e}")
        raise


async def process_pr(pr_number: int, github_client: GitHubClient, dry_run: bool = False) -> Optional[PRInfo]:
    """
    Process a single PR - fetch details, generate hooks, save artifacts.
    
    Args:
        pr_number: PR number to process
        github_client: Initialized GitHubClient
        dry_run: If True, don't execute actual commands
        
    Returns:
        PRInfo object or None if processing failed
    """
    logger.info(f"Processing PR #{pr_number}")
    
    # Step 1: Get PR details
    try:
        pr_details = github_client.get_pr_details(pr_number)
        if not pr_details:
            logger.error(f"Could not fetch details for PR #{pr_number}")
            return None
        
        # Get PR diff
        pr_diff = github_client.get_pr_diff(pr_number)
        if not pr_diff:
            logger.error(f"Could not fetch diff for PR #{pr_number}")
            return None
        
        # Create PRInfo object
        pr_info = PRInfo(
            number=pr_number,
            title=pr_details.title,
            description=pr_details.body,
            diff=pr_diff
        )
        
        # Create summary for large diffs
        if len(pr_diff) > 10 * 1024:  # 10 KB
            pr_info.diff_summary = create_diff_summary(pr_diff)
        
        # Set up output directory
        pr_info.output_dir = setup_output_directory(pr_number)
        logger.info(f"Created output directory: {pr_info.output_dir}")
 
        input_paths = get_input_files(pr_info)
        # Run hook generation
        await run_generate_hooks(pr_info, input_paths, dry_run)
        logger.info(f"Completed processing PR #{pr_number}")
        
        return pr_info
    
    except GitHubError as e:
        logger.error(f"GitHub error processing PR #{pr_number}: {e}")
        return None
    except Exception as e:
        logger.error(f"Unexpected error processing PR #{pr_number}: {e}", exc_info=True)
        return None


def main():
    """Main function to parse args and run the script"""
    parser = argparse.ArgumentParser(description='Process GitHub PRs and generate test hooks')
    parser.add_argument('pr_numbers', type=int, nargs='+', help='PR numbers to process')
    parser.add_argument('--repo', default=DEFAULT_REPO, help='GitHub repository name (owner/repo)')
    parser.add_argument('--dry-run', action='store_true', help='Do not execute hooks generation, just prepare inputs')
    parser.add_argument('--token', help='GitHub API token (or set GITHUB_PAT_TOKEN environment variable)')
    parser.add_argument('--verbose', '-v', action='store_true', help='Enable verbose logging')
    args = parser.parse_args()
    
    # Set log level
    if args.verbose:
        logger.setLevel(logging.DEBUG)
    
    # Create base output directory
    ARTIFACTS_BASE_DIR.mkdir(exist_ok=True, parents=True)
    
    # Initialize GitHub client
    try:
        github_client = GitHubClient(repo_full_name=args.repo, token=args.token)
    except ValueError as e:
        logger.error(f"Failed to initialize GitHub client: {e}")
        sys.exit(1)
    
    # Process each PR
    processed_prs = []
    for pr_number in args.pr_numbers:
        try:
            # Use asyncio.run to handle async function
            pr_info = asyncio.run(process_pr(pr_number, github_client, args.dry_run))
            if pr_info:
                processed_prs.append(pr_info)
        except Exception as e:
            logger.error(f"Failed to process PR #{pr_number}: {e}")
    
    # Summary
    logger.info(f"Processed {len(processed_prs)}/{len(args.pr_numbers)} PRs successfully")
    for pr_info in processed_prs:
        logger.info(f"PR #{pr_info.number}: {pr_info.title} - Output in {pr_info.output_dir}")


if __name__ == '__main__':
    main()
