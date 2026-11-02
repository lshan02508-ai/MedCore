import os

from dotenv import load_dotenv
from sqlalchemy import create_engine
from sqlalchemy.engine import URL
from sqlalchemy.orm import Session, sessionmaker

load_dotenv()

# 1. 构建数据库连接地址
DATABASE_URL = URL.create(
    drivername="mysql+pymysql",
    username=os.environ["MYSQL_USER"],
    password=os.environ["MYSQL_PASSWORD"],
    host=os.getenv("MYSQL_HOST", "127.0.0.1"),
    port=int(os.getenv("MYSQL_PORT", "3306")),
    database=os.getenv("MYSQL_DATABASE", "medcore"),
    query={"charset": "utf8mb4"},
)

# 2. 创建 Engine
engine = create_engine(
    DATABASE_URL,
    pool_pre_ping=True,
)

# 3. 创建 Session 工厂
SessionLocal = sessionmaker(
    bind=engine,
    autoflush=False,
    expire_on_commit=False,
)

# 4. 提供数据库 Session
def get_db():
    with SessionLocal() as session:
        yield session