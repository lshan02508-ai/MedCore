
from typing import NotRequired, TypedDict


class EducationState(TypedDict):
    """
    Represents the state of the education workflow.
    """
    question: str
    history: list[dict[str, str]]

    tool_calls: NotRequired[list[dict[str, str]]]
    sources: NotRequired[list[dict]]
    answer: NotRequired[str]
