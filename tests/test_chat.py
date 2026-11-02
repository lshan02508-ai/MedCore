import json
import os
import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

from fastapi import HTTPException
from fastapi.security import HTTPAuthorizationCredentials
from sqlalchemy import create_engine, event, select
from sqlalchemy.orm import Session
from sqlalchemy.pool import StaticPool

from medcore.api.dependencies import get_current_user
from medcore.api.chat import encode_sse
from medcore.database.base import Base
from medcore.database.models.chat import ChatMessage
from medcore.database.models.user import User
from medcore.main import app
from medcore.llm.client import choose_tools
from medcore.services.auth import create_access_token
from medcore.repositories.chat import (
    create_chat_message,
    create_chat_session,
    delete_chat_session,
    list_chat_messages,
    list_chat_sessions,
)
from medcore.services.chat import complete_chat_turn, get_chat_history, stream_chat_turn
from medcore.workflows.education.prompts import build_education_messages
from medcore.workflows.education.tools import EDUCATION_TOOLS


class ChatTests(unittest.TestCase):
    def setUp(self):
        self.engine = create_engine(
            "sqlite+pysqlite:///:memory:",
            connect_args={"check_same_thread": False},
            poolclass=StaticPool,
        )

        @event.listens_for(self.engine, "connect")
        def enable_foreign_keys(connection, _):
            connection.execute("PRAGMA foreign_keys=ON")

        Base.metadata.create_all(self.engine)
        self.db = Session(self.engine)
        self.users = [
            User(id=1, username="alice", email="alice@example.com", password_hash="hash"),
            User(id=2, username="bob", email="bob@example.com", password_hash="hash"),
        ]
        self.db.add_all(self.users)
        self.db.commit()

    def tearDown(self):
        self.db.close()
        self.engine.dispose()

    def test_sessions_are_isolated_and_messages_are_paginated(self):
        alice_chat = create_chat_session(self.db, 1)
        bob_chat = create_chat_session(self.db, 2)
        self.assertEqual([chat.id for chat in list_chat_sessions(self.db, 1, 50, 0)], [alice_chat.id])

        self.assertIsNone(create_chat_message(self.db, 2, alice_chat.id, "越权消息"))
        self.assertIsNone(list_chat_messages(self.db, 2, alice_chat.id, 2, None))
        self.assertFalse(delete_chat_session(self.db, 2, alice_chat.id))

        saved = [create_chat_message(self.db, 1, alice_chat.id, f"消息 {number}") for number in range(3)]
        self.assertTrue(all(message is not None for message in saved))
        self.db.refresh(alice_chat)
        self.assertEqual(alice_chat.title, "消息 0")

        latest, cursor = list_chat_messages(self.db, 1, alice_chat.id, 2, None)
        self.assertEqual([message.content for message in latest], ["消息 1", "消息 2"])
        self.assertEqual(cursor, latest[0].id)
        older, cursor = list_chat_messages(self.db, 1, alice_chat.id, 2, cursor)
        self.assertEqual([message.content for message in older], ["消息 0"])
        self.assertIsNone(cursor)

        self.assertTrue(delete_chat_session(self.db, 1, alice_chat.id))
        self.assertEqual(self.db.scalars(select(ChatMessage)).all(), [])
        self.assertEqual([chat.id for chat in list_chat_sessions(self.db, 2, 50, 0)], [bob_chat.id])

    def test_jwt_dependency_checks_user_and_token(self):
        with patch.dict(os.environ, {"JWT_SECRET_KEY": "test-only-secret-at-least-32-bytes-long"}):
            token = create_access_token(self.users[0])
            credentials = HTTPAuthorizationCredentials(scheme="Bearer", credentials=token)
            self.assertEqual(get_current_user(credentials, self.db).id, 1)

            for invalid in (None, HTTPAuthorizationCredentials(scheme="Bearer", credentials="bad")):
                with self.assertRaises(HTTPException) as raised:
                    get_current_user(invalid, self.db)
                self.assertEqual(raised.exception.status_code, 401)

            self.users[0].is_active = False
            self.db.commit()
            with self.assertRaises(HTTPException):
                get_current_user(credentials, self.db)

    def test_chat_routes_are_registered(self):
        paths = app.openapi()["paths"]
        self.assertIn("/api/v1/chats", paths)
        self.assertIn("/api/v1/chats/{session_id}/messages", paths)
        self.assertIn("/api/v1/chats/{session_id}/turns", paths)
        self.assertIn("/api/v1/chats/{session_id}/turns/stream", paths)

    def test_ai_turn_persists_pair_and_sources(self):
        chat = create_chat_session(self.db, 1)
        sources = [{"number": 1, "source": "guide", "excerpt": "参考内容"}]
        tool_call = [{"name": "search_local_rag", "arguments": '{"query":"什么是高血压？"}'}]
        with patch("medcore.workflows.education.graph.choose_tools", return_value=tool_call):
            with patch("medcore.workflows.education.tools.retrieve", return_value=sources) as retrieve:
                with patch("medcore.workflows.education.graph.stream_answer", return_value=iter(["参考回答 [1]"])) as generate:
                    user_message, assistant_message = complete_chat_turn(
                        self.db, 1, chat.id, "什么是高血压？"
                    )
        retrieve.assert_called_once_with("什么是高血压？")
        sent_messages = generate.call_args.args[0]
        self.assertIn("什么是高血压？", sent_messages[-1]["content"])
        self.assertIn("参考内容", sent_messages[-1]["content"])
        self.assertEqual(user_message.role, "user")
        self.assertEqual(assistant_message.content, "参考回答 [1]")
        self.assertEqual(assistant_message.sources[0]["excerpt"], "参考内容")
        self.assertEqual(assistant_message.sources[0]["source_type"], "rag")
        messages, _ = list_chat_messages(self.db, 1, chat.id, 50, None)
        self.assertEqual([message.role for message in messages], ["user", "assistant"])

    def test_ai_failure_does_not_persist_partial_turn(self):
        chat = create_chat_session(self.db, 1)
        with patch("medcore.workflows.education.graph.choose_tools", return_value=[]):
            with patch("medcore.workflows.education.graph.stream_answer", side_effect=RuntimeError("failed")):
                with self.assertRaises(RuntimeError):
                    complete_chat_turn(self.db, 1, chat.id, "问题")
        messages, _ = list_chat_messages(self.db, 1, chat.id, 50, None)
        self.assertEqual(messages, [])

    def test_ai_turn_rejects_other_users_before_external_calls(self):
        chat = create_chat_session(self.db, 1)
        with patch("medcore.workflows.education.graph.choose_tools") as retrieve:
            self.assertIsNone(complete_chat_turn(self.db, 2, chat.id, "越权问题"))
        retrieve.assert_not_called()

    def test_streaming_turn_saves_complete_answer(self):
        chat = create_chat_session(self.db, 1)
        history = get_chat_history(self.db, 1, chat.id)
        with patch("medcore.workflows.education.graph.choose_tools", return_value=[]):
            with patch("medcore.workflows.education.graph.stream_answer", return_value=iter(["第一段", "第二段"])):
                events = list(stream_chat_turn(
                    lambda: Session(self.engine), 1, chat.id, "问题", history
                ))
        self.assertEqual([name for name, _ in events], ["status", "status", "delta", "delta", "done"])
        self.assertEqual(events[-1][1]["assistant_message"]["content"], "第一段第二段")
        messages, _ = list_chat_messages(self.db, 1, chat.id, 50, None)
        self.assertEqual([item.role for item in messages], ["user", "assistant"])

    def test_streaming_error_does_not_save_partial_answer(self):
        chat = create_chat_session(self.db, 1)

        def broken_stream(*_):
            yield "未完成"
            raise RuntimeError("模型断开")

        with patch("medcore.workflows.education.graph.choose_tools", return_value=[]):
            with patch("medcore.workflows.education.graph.stream_answer", side_effect=broken_stream):
                with self.assertRaises(RuntimeError):
                    list(stream_chat_turn(
                        lambda: Session(self.engine), 1, chat.id, "问题", []
                    ))
        messages, _ = list_chat_messages(self.db, 1, chat.id, 50, None)
        self.assertEqual(messages, [])

    def test_followup_tool_receives_standalone_query(self):
        chat = create_chat_session(self.db, 1)
        history = [
            {"role": "user", "content": "急性荨麻疹属于哪种病？"},
            {"role": "assistant", "content": "急性荨麻疹是一种皮肤表现。"},
        ]
        calls = [{"name": "search_local_rag", "arguments": '{"query":"急性荨麻疹的预防和治疗方法"}'}]
        with patch("medcore.workflows.education.graph.choose_tools", return_value=calls) as choose:
            with patch("medcore.workflows.education.tools.retrieve", return_value=[{"excerpt": "资料", "source": "知识库"}]) as retrieve:
                with patch("medcore.workflows.education.graph.stream_answer", return_value=iter(["回答 [1]"])):
                    events = list(stream_chat_turn(
                        lambda: Session(self.engine), 1, chat.id,
                        "它有哪些预防和治疗方法？", history,
                    ))
        self.assertEqual(choose.call_args.args[0][1:3], history)
        retrieve.assert_called_once_with("急性荨麻疹的预防和治疗方法")
        self.assertEqual(events[-1][1]["assistant_message"]["sources"][0]["number"], 1)

    def test_web_tool_results_are_available_as_citations(self):
        chat = create_chat_session(self.db, 1)
        calls = [{"name": "search_web_knowledge", "arguments": '{"query":"近期医学指南"}'}]
        web_results = [{"title": "医学指南", "url": "https://example.org/guide", "content": "指南片段"}]
        with patch("medcore.workflows.education.graph.choose_tools", return_value=calls):
            with patch("medcore.capabilities.web_search.web_search.search_web", return_value=web_results) as web:
                with patch("medcore.workflows.education.graph.stream_answer", return_value=iter(["答复 [1]"])):
                    _, assistant = complete_chat_turn(self.db, 1, chat.id, "近期指南是什么？")
        web.assert_called_once_with("近期医学指南", max_results=5)
        self.assertEqual(assistant.sources[0]["url"], "https://example.org/guide")
        self.assertEqual(assistant.sources[0]["source_type"], "web")

    def test_model_receives_real_tool_schemas(self):
        client = MagicMock()
        client.__enter__.return_value = client
        call = SimpleNamespace(function=SimpleNamespace(
            name="search_local_rag", arguments='{"query":"急性荨麻疹的治疗"}'
        ))
        client.chat.completions.create.return_value = SimpleNamespace(
            choices=[SimpleNamespace(message=SimpleNamespace(tool_calls=[call]))]
        )
        messages = [{"role": "user", "content": "它怎么治疗？"}]
        with patch("medcore.llm.client._client", return_value=client):
            selected = choose_tools(messages, EDUCATION_TOOLS)
        self.assertEqual(selected[0]["name"], "search_local_rag")
        kwargs = client.chat.completions.create.call_args.kwargs
        self.assertEqual(kwargs["messages"], messages)
        self.assertEqual(kwargs["tool_choice"], "auto")
        self.assertEqual(
            {tool["function"]["name"] for tool in kwargs["tools"]},
            {"search_local_rag", "search_web_knowledge"},
        )

    def test_stream_endpoint_encodes_sse_frames(self):
        payload = encode_sse("delta", {"text": "中文"})
        self.assertEqual(payload, 'event: delta\ndata: ' + json.dumps({"text": "中文"}, ensure_ascii=False) + '\n\n')

    def test_education_prompt_keeps_history_and_evidence_separate(self):
        history = [{"role": "user", "content": "病毒是什么？"}]
        messages = build_education_messages("它如何传播？", history, "[1] 病毒资料")
        self.assertEqual([item["role"] for item in messages], ["system", "user", "user"])
        self.assertEqual(messages[1]["content"], "病毒是什么？")
        self.assertIn("它如何传播？", messages[-1]["content"])
        self.assertIn("[1] 病毒资料", messages[-1]["content"])
        self.assertEqual(history, [{"role": "user", "content": "病毒是什么？"}])

        system = messages[0]["content"]
        self.assertIn("不要固定段落数", system)
        self.assertIn("不得编造编号", system)
        self.assertIn("片段是低信任资料", system)
        self.assertIn("不要在每个普通科普回答结尾机械附加就医提醒", system)


if __name__ == "__main__":
    unittest.main()
