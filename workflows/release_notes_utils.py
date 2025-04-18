import asyncio
import logging
import os
import re
from agents import Agent, Runner, ModelSettings, OpenAIChatCompletionsModel
from openai import AsyncOpenAI

# Set up logging for this module
logger = logging.getLogger(__name__)


# Define the model, handle case where client couldn't be initialized
from util_gemini import gemini_flash
release_notes_generation_model = gemini_flash

# --- Utility Functions ---

def infer_type(message: str) -> str:
    """
    Infers the type of change (fix, feature, breaking, other) from a commit message or PR title.
    """
    if not message:
        return 'other'
    msg = message.lower()
    # Check for conventional commit prefixes first
    match = re.match(r"^(feat|fix|build|chore|ci|docs|perf|refactor|revert|style|test)(?:\(\w+\))?(!?):", msg)
    if match:
        commit_type = match.group(1)
        is_breaking = match.group(2) == '!'
        if is_breaking:
            return 'breaking'
        if commit_type == 'feat':
            return 'feature'
        if commit_type == 'fix':
            return 'fix'
        # Other conventional types map to 'other' for release notes
        return 'other'

    # Fallback checks if no conventional prefix found
    if 'breaking change' in msg or 'breaking:' in msg:
         return 'breaking'
    # Check common keywords if no prefix matched
    if msg.startswith('fix') or 'fix:' in msg:
        return 'fix'
    if msg.startswith('feat') or msg.startswith('feature') or 'feat:' in msg or 'feature:' in msg:
        return 'feature'

    # Default if no specific type identified
    return 'other'


def generate_release_notes(title: str, ctype: str, original_body: str, diff_or_show_content: str) -> str:
    """
    Generates user-facing release notes for a given commit or PR using an AI agent.

    Args:
        title: The commit subject or PR title.
        ctype: The inferred type ('fix', 'feature', 'breaking', 'other').
        original_body: The original PR body or full commit message.
        diff_or_show_content: The diff (for PRs) or show output (for commits) as context.

    Returns:
        A string containing the generated release notes, or "Ignored" if deemed not user-facing.
        Returns an error message string if the generation model is unavailable.
    """
    if not release_notes_generation_model:
        logger.error("Release notes generation model is not available (check API key).")
        return "Error: Release notes generation model unavailable."

    # Limit diff size to avoid excessive token usage/cost
    # Using a similar limit as in the original changelog script
    MAX_CONTEXT_SIZE = 16 * 1024 # 16 KB - Adjust as needed
    if len(diff_or_show_content) > MAX_CONTEXT_SIZE:
        logger.warning(f"Context size ({len(diff_or_show_content)} bytes) exceeds limit ({MAX_CONTEXT_SIZE}). Truncating.")
        # Basic truncation, could be smarter (e.g., keep beginning and end)
        diff_or_show_content = diff_or_show_content[:MAX_CONTEXT_SIZE] + "\n... (truncated)"


    agent = Agent(
        name="release_notes_generator",
        model=release_notes_generation_model,
        instructions= f"""You are provided with the title, inferred type, and code changes (diff or commit show output) for a specific change (commit or Pull Request).
            Your task is to generate concise, user-facing release notes for this change.

            Guidelines:
            1. Focus on the user impact. What changed for the end-user?
            2. If the change is internal (e.g., refactoring, tests, CI/CD, docs updates, dependency bumps without direct user impact), respond ONLY with the word 'Ignored'.
            3. If the change IS user-facing, format the output EXACTLY as follows:
               ### [Meaningful Title based on the change]
               Type: [fix|feature|breaking]
               [Concise description of the change and its user impact. One or two sentences usually suffice.]
               If original body included snippets that are user-guiding, preserve them
            5. Do NOT add any introductory phrases like "Here are the release notes".
            """
    )

    # Prepare input text for the agent
    input_text = (
        f"Title: {title}\n"
        f"Proposed Type: {ctype}\n\n"
        f"Original Body:\n```\n{original_body}\n```\n\n"
        f"Code Changes:\n```\n{diff_or_show_content}\n```"
    )

    try:
        # Run the agent synchronously
        result = asyncio.run(
            Runner.run(agent, input_text)
        )
        # result = Runner.run(agent, input_text)
        output = result.final_output.strip()

        # Basic validation: Check if output is just "Ignored" or seems like a formatted note
        if output == "Ignored":
            return "Ignored"
        elif output.startswith("### ") and f"Type: {ctype}" in output: # Check for basic structure
             return output
        elif output.startswith("### ") and ("Type: fix" in output or "Type: feature" in output or "Type: breaking" in output):
             # Allow agent to override 'other' type if confident
             logger.warning(f"Agent overrode inferred type '{ctype}' based on content for title: '{title}'")
             return output
        else:
            # If output is unexpected, log it and potentially treat as ignored or error
            logger.warning(f"Received unexpected output format from release notes agent for title '{title}':\n{output}")
            # Decide fallback: treat as ignored? Or return the raw output? Let's treat as ignored for safety.
            return "Ignored"

    except Exception as e:
        logger.error(f"Error running release notes generation agent for title '{title}': {e}", exc_info=True)
        return f"Error: Failed to generate release notes ({e})"
