import json
import logging
from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, Query, Response
from fastapi.responses import StreamingResponse
from sqlalchemy.orm import Session

from medcore.api.dependencies import get_current_user
from medcore.database.connection import SessionLocal, get_db
from medcore.database.models.user import User
from medcore.schemas.chat import (
    ChatMessageCreate,
    ChatMessagePage,
    ChatMessageResponse,
    ChatSessionCreate,
    ChatSessionResponse,
    ChatTurnResponse,
)
from medcore.repositories import chat as chat_repository
from medcore.services.chat import complete_chat_turn, get_chat_history, stream_chat_turn


router = APIRouter(prefix="/api/v1/chats", tags=["chats"])
logger = logging.getLogger(__name__)
Database = Annotated[Session, Depends(get_db)]
CurrentUser = Annotated[User, Depends(get_current_user)]


def encode_sse(name: str, payload: dict) -> str:
    return f"event: {name}\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n"


@router.post("", response_model=ChatSessionResponse, status_code=201)
def create_session(data: ChatSessionCreate, db: Database, user: CurrentUser):
    return chat_repository.create_chat_session(db, user.id, data.title)


@router.get("", response_model=list[ChatSessionResponse])
def list_sessions(
    db: Database,
    user: CurrentUser,
    limit: Annotated[int, Query(ge=1, le=100)] = 50,
    offset: Annotated[int, Query(ge=0)] = 0,
):
    return chat_repository.list_chat_sessions(db, user.id, limit, offset)


@router.delete("/{session_id}", status_code=204)
def delete_session(session_id: int, db: Database, user: CurrentUser):
    if not chat_repository.delete_chat_session(db, user.id, session_id):
        raise HTTPException(status_code=404, detail="会话不存在")
    return Response(status_code=204)


@router.get("/{session_id}/messages", response_model=ChatMessagePage)
def list_messages(
    session_id: int,
    db: Database,
    user: CurrentUser,
    limit: Annotated[int, Query(ge=1, le=100)] = 50,
    before_id: Annotated[int | None, Query(gt=0)] = None,
):
    result = chat_repository.list_chat_messages(db, user.id, session_id, limit, before_id)
    if result is None:
        raise HTTPException(status_code=404, detail="会话不存在")
    items, next_before_id = result
    return ChatMessagePage(items=items, next_before_id=next_before_id) # type: ignore


@router.post("/{session_id}/messages", response_model=ChatMessageResponse, status_code=201)
def send_message(
    session_id: int, data: ChatMessageCreate, db: Database, user: CurrentUser
):
    message = chat_repository.create_chat_message(db, user.id, session_id, data.content)
    if message is None:
        raise HTTPException(status_code=404, detail="会话不存在")
    return message


@router.post("/{session_id}/turns", response_model=ChatTurnResponse, status_code=201)
def send_turn(session_id: int, data: ChatMessageCreate, db: Database, user: CurrentUser):
    try:
        result = complete_chat_turn(db, user.id, session_id, data.content)
    except RuntimeError as exc:
        logger.exception("Chat turn configuration or generation failed")
        if "DEEPSEEK_API_KEY" in str(exc):
            raise HTTPException(status_code=503, detail="AI 服务未配置 API Key") from exc
        raise HTTPException(status_code=502, detail="AI 回复暂时不可用，请稍后重试") from exc
    except Exception as exc:
        logger.exception("Chat turn failed")
        raise HTTPException(status_code=502, detail="知识库或 AI 服务暂时不可用，请稍后重试") from exc
    if result is None:
        raise HTTPException(status_code=404, detail="会话不存在")
    return ChatTurnResponse(user_message=result[0], assistant_message=result[1]) # type: ignore


@router.post("/{session_id}/turns/stream")
def send_turn_stream(session_id: int, data: ChatMessageCreate, db: Database, user: CurrentUser):
    history = get_chat_history(db, user.id, session_id)
    if history is None:
        raise HTTPException(status_code=404, detail="会话不存在")

    def events():
        try:
            for name, payload in stream_chat_turn(
                SessionLocal, user.id, session_id, data.content, history
            ):
                yield encode_sse(name, payload)
        except Exception:
            logger.exception("Streaming chat turn failed")
            yield encode_sse("error", {"detail": "回复中断，消息未保存，请重试"})

    return StreamingResponse(
        events(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )
