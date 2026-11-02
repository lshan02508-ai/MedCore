import os
import unittest
from datetime import datetime, timezone
from unittest.mock import Mock, patch

import jwt
from fastapi import HTTPException

from medcore.api.auth import login
from medcore.database.models.user import User
from medcore.main import app
from medcore.schemas.auth import LoginRequest
from medcore.services.auth import authenticate_user, password_hasher


class LoginTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.user = User(
            id=42,
            email="user@example.com",
            username="tester",
            password_hash=password_hasher.hash("correct-password"),
            is_active=True,
        )

    def test_auth_routes_are_registered(self):
        routes = app.openapi()["paths"]
        self.assertIn("/auth/register", routes)
        self.assertIn("/auth/login", routes)

    def test_valid_login_returns_signed_expiring_token(self):
        db = Mock()
        db.scalar.return_value = self.user
        request = LoginRequest(email="USER@example.com", password="correct-password")

        test_secret = "test-only-secret-at-least-32-bytes-long"
        with patch.dict(os.environ, {"JWT_SECRET_KEY": test_secret}):
            response = login(request, db)
            claims = jwt.decode(response.access_token, test_secret, algorithms=["HS256"])

        self.assertEqual(response.token_type, "bearer")
        self.assertEqual(claims["sub"], "42")
        seconds_remaining = claims["exp"] - datetime.now(timezone.utc).timestamp()
        self.assertGreater(seconds_remaining, 29 * 60)
        self.assertLessEqual(seconds_remaining, 30 * 60)
        self.assertIs(authenticate_user(db, "USER@example.com", "correct-password"), self.user)

    def test_wrong_password_and_inactive_user_are_rejected(self):
        db = Mock()
        db.scalar.return_value = self.user

        for password, active in (("wrong-password", True), ("correct-password", False)):
            with self.subTest(password=password, active=active):
                self.user.is_active = active
                with self.assertRaises(HTTPException) as raised:
                    login(LoginRequest(email="user@example.com", password=password), db)
                self.assertEqual(raised.exception.status_code, 401)
                self.assertEqual(raised.exception.headers["WWW-Authenticate"], "Bearer")

        self.user.is_active = True

    def test_missing_user_is_rejected(self):
        db = Mock()
        db.scalar.return_value = None
        with self.assertRaises(HTTPException) as raised:
            login(LoginRequest(email="user@example.com", password="any-password"), db)
        self.assertEqual(raised.exception.status_code, 401)

    def test_missing_signing_secret_is_rejected(self):
        db = Mock()
        db.scalar.return_value = self.user
        with patch.dict(os.environ, {"JWT_SECRET_KEY": ""}):
            with self.assertRaisesRegex(RuntimeError, "JWT_SECRET_KEY 未配置"):
                login(LoginRequest(email="user@example.com", password="correct-password"), db)


if __name__ == "__main__":
    unittest.main()
