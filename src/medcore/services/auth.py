"""User registration, authentication, and access tokens."""

import os
from datetime import datetime, timedelta, timezone

import jwt
from dotenv import load_dotenv
from pwdlib import PasswordHash
from sqlalchemy import select
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session

from medcore.database.models.user import User
from medcore.schemas.auth import RegisterRequest


password_hasher = PasswordHash.recommended()
load_dotenv()
ACCESS_TOKEN_EXPIRE_MINUTES = 30


def register_user(db: Session, data: RegisterRequest) -> User:
    user = User(
        username=data.username,
        email=str(data.email).lower(),
        password_hash=password_hasher.hash(data.password),
    )

    db.add(user)

    try:
        db.commit()
    except IntegrityError as exc:
        db.rollback()
        raise ValueError("用户名或邮箱已被使用") from exc

    db.refresh(user)
    return user


def authenticate_user(
    db: Session,
    email: str,
    password: str,
) -> User | None:
    user = db.scalar(select(User).where(User.email == email.lower()))

    if user is None:
        return None

    if not password_hasher.verify(password, user.password_hash):
        return None

    if not user.is_active:
        return None

    return user


def create_access_token(user: User) -> str:
    secret_key = os.getenv("JWT_SECRET_KEY")
    if not secret_key:
        raise RuntimeError("JWT_SECRET_KEY 未配置")

    expires_at = datetime.now(timezone.utc) + timedelta(
        minutes=ACCESS_TOKEN_EXPIRE_MINUTES
    )
    return jwt.encode(
        {"sub": str(user.id), "exp": expires_at},
        secret_key,
        algorithm="HS256",
    )
