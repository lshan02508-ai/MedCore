def main() -> None:
    import uvicorn

    uvicorn.run("medcore.main:app", host="127.0.0.1", port=8089)
