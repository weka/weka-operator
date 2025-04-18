import logging
import os

from agents import OpenAIChatCompletionsModel
from openai import AsyncOpenAI

logger = logging.getLogger(__name__)

# Aggregation Model Setup (assuming it might be different or needs separate config)
# If it's the same model, this could potentially be imported too, but let's keep it separate for now.
GEMINI_API_KEY = os.getenv("GEMINI_API_KEY")  # Still needed if aggregation uses Gemini
if not GEMINI_API_KEY:
    raise Exception("cannot initialize final release notes aggregation model due to missing API key.")

gemini_client_agg = AsyncOpenAI(base_url="https://generativelanguage.googleapis.com/v1beta/openai/",
                                api_key=GEMINI_API_KEY)
final_release_notes_model = OpenAIChatCompletionsModel(
    model="gemini-2.5-pro-preview-03-25",  # Or potentially a larger/different model for aggregation
    # model="gemini-2.5-flash-preview-04-17", # Or potentially a larger/different model for aggregation
    openai_client=gemini_client_agg
)

gemini_pro = OpenAIChatCompletionsModel(
    model="gemini-2.5-pro-preview-03-25",  # Or potentially a larger/different model for aggregation
    openai_client=gemini_client_agg
)

gemini_flash = OpenAIChatCompletionsModel(
    model="gemini-2.5-flash-preview-04-17",
    openai_client=gemini_client_agg
)
