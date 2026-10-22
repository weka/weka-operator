import asyncio
import atexit
import logging
import os

from agents import OpenAIChatCompletionsModel
from openai import AsyncOpenAI

logger = logging.getLogger(__name__)

_runner = None


def run_sync(coro):
    """Run a coroutine on one process-wide event loop.

    The module-level AsyncOpenAI client pools keep-alive connections bound to the
    loop that opened them. asyncio.run() per call closes that loop, so a later
    call reusing a pooled connection closes it against a dead loop and raises
    "Event loop is closed".
    """
    global _runner
    if _runner is None:
        _runner = asyncio.Runner()
    return _runner.run(coro)


@atexit.register
def _close_runner():
    if _runner is not None:
        _runner.close()

# Aggregation Model Setup (assuming it might be different or needs separate config)
# If it's the same model, this could potentially be imported too, but let's keep it separate for now.
GEMINI_API_KEY = os.getenv("GEMINI_API_KEY")  # Still needed if aggregation uses Gemini
if not GEMINI_API_KEY:
    raise Exception("cannot initialize final release notes aggregation model due to missing API key.")

gemini_client_agg = AsyncOpenAI(base_url="https://generativelanguage.googleapis.com/v1beta/openai/",
                                api_key=GEMINI_API_KEY)
final_release_notes_model = OpenAIChatCompletionsModel(
    model="gemini-3.5-flash",
    openai_client=gemini_client_agg
)

gemini_flash = OpenAIChatCompletionsModel(
    model="gemini-2.5-flash",
    openai_client=gemini_client_agg
)
