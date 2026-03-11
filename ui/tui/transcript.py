def render_transcript(
    messages: list[dict[str, str]],
    pending_assistant: str,
    waiting_for_first_chunk: bool,
    spinner_frame: str,
) -> str:
    blocks: list[str] = []

    for message in messages:
        role = message["role"]
        content = message["content"]
        if role == "user":
            blocks.append(format_user_block(content))
        elif role == "assistant":
            blocks.append(content)

    if waiting_for_first_chunk:
        blocks.append(f"{spinner_frame} Thinking")

    if pending_assistant:
        blocks.append(pending_assistant)

    return "\n\n".join(blocks)


def format_user_block(content: str) -> str:
    lines = content.splitlines() or [content]
    return "\n".join(f"> {line}" if line else ">" for line in lines)
