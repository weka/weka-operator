#!/usr/bin/env -S uv run --no-project --with requests
"""
Wekai Remote Execution Wrapper with GitHub Integration

This script wraps wekai remote-bot execution and provides:
1. JSON output parsing from wekai --json-output
2. Rich GitHub PR comment creation with execution details
3. URL tracking and workflow integration
4. Usage metrics and status reporting

Usage:
    # In CI (recommended - uses GITHUB_TOKEN from environment):
    python run_wekai_remote.py --request-file plan.txt --docs-dir /path/to/docs 
        --params cluster_name=test --params namespace=default
        --wekai-path /path/to/wekai --pr-number 123
    
    # For local testing (fallback):
    python run_wekai_remote.py --request-file plan.txt --docs-dir /path/to/docs 
        --params cluster_name=test --params namespace=default
        --wekai-path /path/to/wekai --pr-number 123 --gh-token $GH_TOKEN
"""

import argparse
import json
import os
import subprocess
import sys
import requests
from pathlib import Path
from typing import Dict, List, Optional
from datetime import datetime


def get_github_token(provided_token: Optional[str]) -> Optional[str]:
    """
    Get GitHub token from environment or provided argument.
    Prioritizes environment variables for security.
    """
    return (
        provided_token or 
        os.environ.get('GITHUB_TOKEN') or 
        os.environ.get('GH_TOKEN')
    )


def run_wekai_with_json_output(
    wekai_path: str,
    request_file: str,
    execution_id: str,
    docs_dir: str,
    params: List[str],
    execution_tmp_dir: Optional[str] = None,
) -> Dict:
    """
    Run wekai in remote-bot mode with JSON output enabled.
    
    Returns:
        Parsed JSON result from wekai
    """
    cmd = [
        wekai_path,
        '--mode', 'remote-bot',
        '--json-output',
        '--stderr-logs',
        '--docs-dir', docs_dir,
        '--remote-bot-endpoint', 'https://wekai.scalar.dev.weka.io/api',
        '--remote-bot-worker', 'operator-ci',
        '--execution-id', execution_id,
        '--plan-tags', 'operator-ci',
        '--request-file', request_file
    ]
    if execution_tmp_dir:
        cmd.extend(['--tmp-dir', execution_tmp_dir])
    
    # Add params
    for param in params:
        cmd.extend(['--params', param])
    
    print(f"🚀 Running wekai command: {' '.join(cmd)}")
    
    try:
        # Run wekai and capture output
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=1800)  # 30min timeout
        
        # Print stderr for logging (human-readable progress)
        if result.stderr:
            print("📤 Wekai progress log:")
            print(result.stderr)
        
        if result.returncode != 0:
            print(f"❌ Wekai failed with exit code {result.returncode}")
            print("📤 Wekai stdout:")
            print(result.stdout)
            return {
                "success": False,
                "status": "failed",
                "error_details": f"Wekai process failed with exit code {result.returncode}",
                "result": result.stdout
            }
        
        # Parse JSON output from stdout
        try:
            json_result = json.loads(result.stdout.strip())
            print(f"✅ Successfully parsed wekai JSON output")
            return json_result
        except json.JSONDecodeError as e:
            print(f"❌ Failed to parse JSON output: {e}")
            print("📤 Raw stdout:")
            print(result.stdout)
            return {
                "success": False,
                "status": "json_parse_error", 
                "error_details": f"Failed to parse JSON output: {e}",
                "result": result.stdout
            }
        
    except subprocess.TimeoutExpired:
        print("❌ Wekai execution timed out after 30 minutes")
        return {
            "success": False,
            "status": "timeout",
            "error_details": "Execution timed out after 30 minutes"
        }
    except Exception as e:
        print(f"❌ Error running wekai: {e}")
        return {
            "success": False,
            "status": "error",
            "error_details": str(e)
        }


def format_usage_data(usage_data: Optional[Dict]) -> str:
    """Format usage data for display in GitHub comment."""
    if not usage_data or not usage_data.get('request_count'):
        return ""
    
    total_tokens = (
        usage_data.get('input_tokens', {}).get('count', 0) +
        usage_data.get('output_tokens', {}).get('count', 0) +
        usage_data.get('cached_tokens', {}).get('count', 0) +
        usage_data.get('reasoning_tokens', {}).get('count', 0)
    )
    
    cost = usage_data.get('total_cost', 0)
    requests = usage_data.get('request_count', 0)
    
    return f"**Usage:** {requests} requests, {total_tokens:,} tokens, ${cost:.4f}"


def create_github_comment(
    pr_number: int,
    wekai_result: Dict,
    gh_token: str,
    workflow_url: Optional[str] = None
) -> bool:
    """
    Create or update a GitHub PR comment with comprehensive wekai execution details.
    
    Returns:
        True if successful, False otherwise
    """
    try:
        repo = os.environ.get('GITHUB_REPOSITORY', '')
        if not repo:
            print("❌ GITHUB_REPOSITORY not set, skipping GitHub comment")
            return False
            
        # Get workflow URL from environment if not provided
        if not workflow_url:
            server_url = os.environ.get('GITHUB_SERVER_URL', 'https://github.com')
            run_id = os.environ.get('GITHUB_RUN_ID')
            if run_id:
                workflow_url = f"{server_url}/{repo}/actions/runs/{run_id}"
        
        # Extract data from wekai result
        plan_url = wekai_result.get('plan_url')
        execution_url = wekai_result.get('execution_url')
        execution_id = wekai_result.get('execution_id')
        status = wekai_result.get('status', 'unknown')
        success = wekai_result.get('success', False)
        worker_name = wekai_result.get('worker_name', 'operator-ci')
        usage_data = wekai_result.get('usage_data')
        error_details = wekai_result.get('error_details')
        
        # Build rich comment body with execution ID in header
        header = f"## 🤖 WekAI Remote Test Execution"
        if execution_id:
            header += f" #{execution_id}"
        comment_body = f"{header}\n\n"
        
        # Status section with emoji
        status_emojis = {
            "completed": "✅",
            "failed": "❌", 
            "timeout": "⏰",
            "executing": "⏳",
            "planning": "📋",
            "plan_complete": "📋✅",
            "error": "💥",
            "json_parse_error": "🔧",
            "unknown": "❓"
        }
        
        status_emoji = status_emojis.get(status, "❓")
        comment_body += f"**Status:** {status_emoji} {status.title()}\n"
        
        if worker_name:
            comment_body += f"**Worker:** {worker_name}\n"
        
        comment_body += "\n"
        
        # URLs section
        if workflow_url:
            comment_body += f"🔗 **[GitHub Workflow Run]({workflow_url})**\n\n"
        
        if plan_url:
            comment_body += f"📋 **[Test Plan]({plan_url})**\n\n"
        
        if execution_url:
            comment_body += f"🚀 **[Execution Details]({execution_url})**\n\n"
        
        # Usage information
        usage_info = format_usage_data(usage_data)
        if usage_info:
            comment_body += f"{usage_info}\n\n"
        
        # Error details if present
        if error_details:
            comment_body += f"**Error Details:**\n```\n{error_details}\n```\n\n"
        
        # Results preview (truncated for comment length)
        result_content = wekai_result.get('result', '')
        if result_content:
            if len(result_content) > 1000:
                result_preview = result_content[:1000] + "... (truncated)"
            else:
                result_preview = result_content
            
            comment_body += f"**Result Preview:**\n```\n{result_preview}\n```\n\n"
        
        # Footer with timestamp
        timestamp = datetime.utcnow().strftime('%Y-%m-%d %H:%M:%S UTC')
        sha = os.environ.get('GITHUB_SHA', 'unknown')[:7]
        comment_body += f"*Last updated: {timestamp} • Commit: {sha}*"
        
        # GitHub API call - always create a new comment for each execution
        api_url = f"https://api.github.com/repos/{repo}/issues/{pr_number}/comments"
        headers = {
            'Authorization': f'token {gh_token}',
            'Accept': 'application/vnd.github.v3+json'
        }
        
        # Create new comment for each execution
        response = requests.post(api_url, headers=headers, json={'body': comment_body})
        if response.status_code == 201:
            execution_info = f" (execution #{execution_id})" if execution_id else ""
            print(f"✅ Created GitHub PR comment #{pr_number}{execution_info}")
            return True
        else:
            print(f"❌ Failed to create GitHub comment: {response.status_code} - {response.text}")
            return False
            
    except Exception as e:
        print(f"❌ Error creating GitHub comment: {e}")
        return False


def main():
    parser = argparse.ArgumentParser(description="Run wekai remote-bot with GitHub integration")
    parser.add_argument('--wekai-path', required=True, help='Path to wekai executable')
    parser.add_argument('--execution-id', required=True, help='Unique execution ID for tracking')
    parser.add_argument('--execution-tmp-dir', help='Temporary directory for execution files')
    parser.add_argument('--request-file', required=True, help='Path to request file (plan.txt)')
    parser.add_argument('--docs-dir', required=True, help='Path to documentation directory')
    parser.add_argument('--params', action='append', default=[], help='Parameters to pass to wekai')
    parser.add_argument('--pr-number', type=int, help='GitHub PR number for comments')
    parser.add_argument('--gh-token', help='GitHub token (fallback - prefer GITHUB_TOKEN env var)')
    
    args = parser.parse_args()
    
    # Validate inputs
    if not Path(args.wekai_path).exists():
        print(f"❌ Wekai executable not found: {args.wekai_path}")
        return 1
        
    if not Path(args.request_file).exists():
        print(f"❌ Request file not found: {args.request_file}")
        return 1
        
    if not Path(args.docs_dir).exists():
        print(f"❌ Docs directory not found: {args.docs_dir}")
        return 1
    
    if args.execution_tmp_dir and not Path(args.execution_tmp_dir).exists():
        # Create temporary directory if it doesn't exist
        try:
            Path(args.execution_tmp_dir).mkdir(parents=True, exist_ok=True)
            print(f"✅ Created temporary directory: {args.execution_tmp_dir}")
        except Exception as e:
            print(f"❌ Failed to create temporary directory: {e}")
            return 1
    
    # Run wekai and get JSON result
    wekai_result = run_wekai_with_json_output(
        wekai_path=args.wekai_path,
        request_file=args.request_file,
        execution_id=args.execution_id,
        execution_tmp_dir=args.execution_tmp_dir,
        docs_dir=args.docs_dir,
        params=args.params
    )
    
    # Display key results
    print("\n" + "="*60)
    print("📊 WEKAI EXECUTION SUMMARY")
    print("="*60)
    
    status = wekai_result.get('status', 'unknown')
    success = wekai_result.get('success', False)
    
    print(f"Status: {status}")
    print(f"Success: {success}")
    
    if wekai_result.get('plan_url'):
        print(f"Plan URL: {wekai_result['plan_url']}")
    
    if wekai_result.get('execution_url'):
        print(f"Execution URL: {wekai_result['execution_url']}")
    
    if wekai_result.get('worker_name'):
        print(f"Worker: {wekai_result['worker_name']}")
    
    usage_info = format_usage_data(wekai_result.get('usage_data'))
    if usage_info:
        print(usage_info)
    
    # Create GitHub comment if PR number provided
    if args.pr_number:
        gh_token = get_github_token(args.gh_token)
        if gh_token:
            print("\n🔄 Creating GitHub PR comment...")
            create_github_comment(args.pr_number, wekai_result, gh_token)
        else:
            print("⚠️  PR number provided but no GitHub token available")
            print("    Set GITHUB_TOKEN environment variable or use --gh-token flag")
    
    # Return appropriate exit code
    if not success:
        print(f"\n❌ Wekai execution was not successful")
        # even if the execution failed, we still return 0 to avoid breaking the workflow
        return 0
    
    print(f"\n✅ Wekai execution completed successfully")
    return 0


if __name__ == '__main__':
    sys.exit(main())
