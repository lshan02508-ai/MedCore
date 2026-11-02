"""Persistence operations for chat sessions and messages."""

from sqlalchemy import func, select
from sqlalchemy.orm import Session

from medcore.database.models.chat import ChatMessage, ChatSession


def create_chat_session(db: Session, user_id: int, title: str = "新对话") -> ChatSession:
    chat = ChatSession(user_id=user_id, title=title)
    db.add(chat)
    db.commit()
    db.refresh(chat)
    return chat


def list_chat_sessions(
    db: Session, user_id: int, limit: int, offset: int
) -> list[ChatSession]:
    return list(
        db.scalars(
            select(ChatSession)
            .where(ChatSession.user_id == user_id)
            .order_by(ChatSession.updated_at.desc(), ChatSession.id.desc())
            .limit(limit)
            .offset(offset)
        ).all()
    )


def get_chat_session(db: Session, user_id: int, session_id: int) -> ChatSession | None:
    return db.scalar(
        select(ChatSession).where(
            ChatSession.id == session_id, ChatSession.user_id == user_id
        )
    )


def delete_chat_session(db: Session, user_id: int, session_id: int) -> bool:
    chat = get_chat_session(db, user_id, session_id)
    if chat is None:
        return False
    db.delete(chat)
    db.commit()
    return True


def list_chat_messages(
    db: Session, user_id: int, session_id: int, limit: int, before_id: int | None
) -> tuple[list[ChatMessage], int | None] | None:
    if get_chat_session(db, user_id, session_id) is None:
        return None

    query = select(ChatMessage).where(ChatMessage.session_id == session_id)
    if before_id is not None:
        query = query.where(ChatMessage.id < before_id)
    newest_first = list(
        db.scalars(query.order_by(ChatMessage.id.desc()).limit(limit + 1)).all()
    )
    has_more = len(newest_first) > limit
    items = list(reversed(newest_first[:limit]))
    next_before_id = items[0].id if has_more else None
    return items, next_before_id


def create_chat_message(
    db: Session, user_id: int, session_id: int, content: str
) -> ChatMessage | None:
    chat = db.scalar(
        select(ChatSession)
        .where(ChatSession.id == session_id, ChatSession.user_id == user_id)
        .with_for_update()
    )
    if chat is None:
        return None

    message = ChatMessage(session_id=session_id, role="user", content=content)
    db.add(message)
    if chat.title == "新对话":
        chat.title = content[:40]
    chat.updated_at = func.now()
    db.commit()
    db.refresh(message)
    return message


def save_chat_turn(
    db: Session, user_id: int, session_id: int, question: str, answer: str,
    sources: list[dict],
) -> tuple[ChatMessage, ChatMessage] | None:
    chat = db.scalar(
        select(ChatSession)
        .where(ChatSession.id == session_id, ChatSession.user_id == user_id)
        .with_for_update()
    )
    if chat is None:
        return None
    user_message = ChatMessage(session_id=session_id, role="user", content=question)
    assistant_message = ChatMessage(
        session_id=session_id, role="assistant", content=answer, sources=sources
    )
    db.add_all([user_message, assistant_message])
    if chat.title == "新对话":
        chat.title = question[:40]
    chat.updated_at = func.now()
    db.commit()
    db.refresh(user_message)
    db.refresh(assistant_message)
    return user_message, assistant_message
