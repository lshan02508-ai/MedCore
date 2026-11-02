"""Complete a chat turn using retrieval, generation, and persistence."""

import re

from sqlalchemy import select
from sqlalchemy.orm import Session

from medcore.database.models.chat import ChatMessage
from medcore.repositories.chat import get_chat_session, save_chat_turn
from medcore.schemas.chat import ChatTurnResponse
from medcore.workflows.education.graph import build_education_graph

education_graph = build_education_graph()


def _cited_sources(answer: str, sources: list[dict]) -> list[dict]:
    """Only expose retrieved documents actually cited in the answer."""
    cited = {int(number) for number in re.findall(r"\[(\d+)\]", answer)}
    return [source for source in sources if source.get("number") in cited]


def get_chat_history(
    db: Session, user_id: int, session_id: int
) -> list[dict[str, str]] | None:
    if get_chat_session(db, user_id, session_id) is None:
        db.rollback()
        return None
    previous = list(db.scalars(
        select(ChatMessage)
        .where(ChatMessage.session_id == session_id)
        .order_by(ChatMessage.id.desc())
        .limit(12)
    ).all())
    history = [
        {"role": message.role, "content": message.content}
        for message in reversed(previous)
        if message.role in ("user", "assistant")
    ]
    db.rollback()  # Do not hold a DB connection during retrieval and model inference.
    return history


def complete_chat_turn(
    db: Session, user_id: int, session_id: int, question: str
) -> tuple[ChatMessage, ChatMessage] | None:
    history = get_chat_history(db, user_id, session_id)
    if history is None:
        return None
    result = education_graph.invoke({"question": question, "history": history})
    return save_chat_turn(
        db, user_id, session_id, question, result["answer"],
        _cited_sources(result["answer"], result.get("sources", [])),
    )


def stream_chat_turn(db_factory, user_id, session_id, question, history):
    sources = []
    answer = None

    for part in education_graph.stream(
        {"question": question, "history": history},
        stream_mode=["custom", "updates"],
        version="v2",
    ):
        if part["type"] == "custom":
            event = part["data"]
            yield event["name"], event["payload"]
        elif part["type"] == "updates":
            updates = part["data"]
            if "tools" in updates:
                sources = updates["tools"]["sources"]
            if "answer" in updates:
                answer = updates["answer"]["answer"]

    if not answer:
        raise RuntimeError("科普工作流未完成")

    with db_factory() as db:
        saved = save_chat_turn(
            db, user_id, session_id, question, answer,
            _cited_sources(answer, sources),
        )
        if saved is None:
            raise RuntimeError("会话已删除，回答未保存")
        turn = ChatTurnResponse(
            user_message=saved[0], assistant_message=saved[1]
        )
        yield "done", turn.model_dump(mode="json")
