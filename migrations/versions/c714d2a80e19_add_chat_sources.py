"""store retrieval sources with assistant messages

Revision ID: c714d2a80e19
Revises: b38f2c1d9a7e
"""

from alembic import op
import sqlalchemy as sa


revision = "c714d2a80e19"
down_revision = "b38f2c1d9a7e"
branch_labels = None
depends_on = None


def upgrade() -> None:
    op.add_column("chat_messages", sa.Column("sources", sa.JSON(), nullable=True))


def downgrade() -> None:
    op.drop_column("chat_messages", "sources")
