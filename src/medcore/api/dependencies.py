import os
from typing import Annotated

import jwt
from fastapi import Depends, HTTPException
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from sqlalchemy.orm import Session

from medcore.database.connection import get_db
from medcore.database.models.user import User


bearer_scheme = HTTPBearer(auto_error=False)


def get_current_user(
    credentials: Annotated[HTTPAuthorizationCredentials | None, Depends(bearer_scheme)],
    db: Annotated[Session, Depends(get_db)],
) -> User:
    unauthorized = HTTPException(
        status_code=401,
        detail="登录已失效，请重新登录",
        headers={"WWW-Authenticate": "Bearer"},
    )
    secret_key = os.getenv("JWT_SECRET_KEY")
    if not credentials or credentials.scheme.lower() != "bearer" or not secret_key:
        raise unauthorized

    try:
        claims = jwt.decode(
            credentials.credentials,
            secret_key,
            algorithms=["HS256"],
            options={"require": ["exp", "sub"]},
        )
        user_id = int(claims["sub"])
        if user_id <= 0:
            raise ValueError("invalid user id")
    except (jwt.InvalidTokenError, ValueError, TypeError) as exc:
        raise unauthorized from exc

    user = db.get(User, user_id)
    if user is None or not user.is_active:
        raise unauthorized
    return user
