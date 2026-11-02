from fastapi import FastAPI
from medcore.api import router
app = FastAPI(
    title="MedCore API",
    description="",
    version="0.1.0"
)

app.include_router(router.apirouter)
