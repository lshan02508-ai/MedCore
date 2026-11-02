from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy.orm import Session

from medcore.database.connection import get_db
from medcore.schemas.auth import LoginRequest, RegisterRequest, TokenResponse, UserResponse
from medcore.services.auth import authenticate_user, create_access_token, register_user

auth_router = APIRouter(prefix="/auth", tags=["auth"])


@auth_router.post("/register", response_model=UserResponse, status_code=201)
def register(data: RegisterRequest, db: Annotated[Session, Depends(get_db)]):
    try:
        return register_user(db=db, data=data)
    except ValueError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc


@auth_router.post("/login", response_model=TokenResponse)
def login(data: LoginRequest, db: Annotated[Session, Depends(get_db)]):
    user = authenticate_user(db, str(data.email), data.password)
    if user is None:
        raise HTTPException(
            status_code=401,
            detail="邮箱或密码错误，或账号已停用",
            headers={"WWW-Authenticate": "Bearer"},
        )

    return TokenResponse(access_token=create_access_token(user))
