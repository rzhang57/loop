from typing import AsyncGenerator

from core_service.providers import client, DEFAULT_MODEL


async def chat(messages: list) -> AsyncGenerator[str, None]:
    stream = await client.chat.completions.create(
        model=DEFAULT_MODEL,
        messages=messages,
        stream=True,
    )

    async for chunk in stream:
        choices = getattr(chunk, "choices", None)
        if not choices:
            continue
        first = choices[0] if len(choices) > 0 else None
        if not first:
            continue
        delta = getattr(first, "delta", None)
        content = getattr(delta, "content", None)
        if content:
            yield content
