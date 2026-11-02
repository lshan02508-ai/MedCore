from fastapi import APIRouter

from medcore.api import auth, chat, health

apirouter = APIRouter(prefix="")
apirouter.include_router(health.router)
apirouter.include_router(auth.auth_router)
apirouter.include_router(chat.router)
