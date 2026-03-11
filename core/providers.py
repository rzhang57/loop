import os
from openai import AsyncOpenAI

from core.config import get_required_env

DEFAULT_MODEL = "stepfun/step-3.5-flash:free"

client = AsyncOpenAI(
    base_url="https://openrouter.ai/api/v1",
    api_key=get_required_env("OPENROUTER_API_KEY"),
)
