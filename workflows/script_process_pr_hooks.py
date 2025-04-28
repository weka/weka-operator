#!/usr/bin/env -S uv run --no-project --with openai-agents
import argparse
import asyncio
import logging
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, Any, Optional, List

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


def get_input_files(pr_infos: List[PRInfo], output_dir: Path) -> InputFilePaths:
    """
    Create necessary input files and return paths for hook generation.
    Directly references the original source locations.
    
    Args:
        pr_infos: List of PRInfo objects with PR details
        output_dir: Directory to save combined PR data
        
    Returns:
        InputFilePaths object containing all required file paths
    """
    # Create a combined PR description and diff
    pr_desc_path = output_dir / "pr_description.txt"
    pr_diff_path = output_dir / "pr_diff.txt"
    
    # Combine all PR descriptions
    description, diff = "", ""
    if len(pr_infos) > 1:
        description = "# Combined PR Information\n\n"
        diff = "# Combined Diffs from Multiple PRs\n\n"
    
    for pr_info in pr_infos:
        description += f"## PR #{pr_info.number}: {pr_info.title}\n\n"
        description += (pr_info.description or "") + "\n\n---\n\n"
        
        # Add diff section for this PR
        diff += f"## Diff for PR #{pr_info.number}: {pr_info.title}\n\n"
        diff_section = generate_diff_section(pr_info.diff, pr_info.diff_summary)
        diff += diff_section + "\n\n---\n\n"
    
    # Save combined PR description and diff
    with open(pr_desc_path, 'w') as f:
        f.write(description)
    
    with open(pr_diff_path, 'w') as f:
        f.write(diff)
    
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
    
    # Create InputFilePaths object with original file paths and combined PR info
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


async def run_generate_hooks(pr_infos: List[PRInfo], input_paths: InputFilePaths, output_dir: Path, dry_run: bool = False) -> None:
    """
    Run the generate_hooks function to process all PR information at once.
    
    Args:
        pr_infos: List of PRInfo objects with PR details
        input_paths: InputFilePaths object with paths to use as input
        output_dir: Directory to save combined hooks
        dry_run: If True, just print what would happen without executing
    """
    if not pr_infos:
        logger.error("No PRs to process")
        return
    
    if dry_run:
        logger.info("[DRY RUN] Would generate hooks with the following paths:")
        for key, path in input_paths.__dict__.items():
            logger.info(f"  {key}: {path}")
        logger.info(f"  Output directory: {output_dir}")
        return
    
    try:
        # Call generate_hooks function directly with InputFilePaths object and output directory
        logger.info(f"Generating hooks for {len(pr_infos)} PRs using combined InputFilePaths")
        await generate_hooks(input_paths=input_paths, output_dir=output_dir)
        logger.info(f"Successfully generated hooks in {output_dir}")
    except Exception as e:
        logger.error(f"Failed to generate hooks: {e}", exc_info=True)
        raise


async def process_prs(pr_numbers: List[int], github_client: GitHubClient, dry_run: bool = False) -> List[PRInfo]:
    """
    Process multiple PRs - fetch details for all, then generate hooks once with combined information.
    
    Args:
        pr_numbers: List of PR numbers to process
        github_client: Initialized GitHubClient
        dry_run: If True, don't execute actual commands
        
    Returns:
        List of PRInfo objects or empty list if processing failed
    """
    logger.info(f"Processing {len(pr_numbers)} PRs: {pr_numbers}")
    
    # Create base output directory
    ARTIFACTS_BASE_DIR.mkdir(exist_ok=True, parents=True)
    
    # First, collect information for all PRs
    pr_infos = []
    
    for pr_number in pr_numbers:
        logger.info(f"Collecting information for PR #{pr_number}")
        try:
            # Get PR details
            pr_details = github_client.get_pr_details(pr_number)
            if not pr_details:
                logger.error(f"Could not fetch details for PR #{pr_number}")
                continue
            
            # Get PR diff
            pr_diff = github_client.get_pr_diff(pr_number)
            if not pr_diff:
                logger.error(f"Could not fetch diff for PR #{pr_number}")
                continue
            
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
            
            pr_infos.append(pr_info)
        
        except GitHubError as e:
            logger.error(f"GitHub error processing PR #{pr_number}: {e}")
        except Exception as e:
            logger.error(f"Unexpected error processing PR #{pr_number}: {e}", exc_info=True)
    
    if not pr_infos:
        logger.error("No valid PRs to process")
        return []
    
    # Now create a combined output directory and combined input files
    output_dir = ARTIFACTS_BASE_DIR
    input_paths = get_input_files(pr_infos, output_dir)
    
    # Run hook generation once with combined PR information
    await run_generate_hooks(pr_infos, input_paths, output_dir, dry_run)
    logger.info(f"Completed processing {len(pr_infos)} PRs with combined information")
    
    return pr_infos


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
    
    # Process all PRs at once
    try:
        processed_prs = asyncio.run(process_prs(args.pr_numbers, github_client, args.dry_run))
        
        # Summary
        if processed_prs:
            logger.info(f"Successfully processed {len(processed_prs)}/{len(args.pr_numbers)} PRs")
            for pr_info in processed_prs:
                logger.info(f"PR #{pr_info.number}: {pr_info.title} - Output in {pr_info.output_dir}")
        else:
            logger.error("Failed to process any PRs")
            sys.exit(1)
    except Exception as e:
        logger.error(f"Failed to process PRs: {e}", exc_info=True)
        sys.exit(1)


if __name__ == '__main__':
    main()
