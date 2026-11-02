from fastapi import APIRouter

router = APIRouter()
@router.get("/check_health")
def health():
    pass