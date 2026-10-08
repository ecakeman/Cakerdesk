from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, Field


class Deliverable(BaseModel):
    path: str
    must_contain: list[str] = Field(default_factory=list)


class ContractOut(BaseModel):
    goal: str
    deliverables: list[Deliverable]


class PlanStepOut(BaseModel):
    id: str
    title: str
    status: str = "pending"
    note: str = ""


class PlanOut(BaseModel):
    goal: str = ""
    steps: list[PlanStepOut]


class MemoryItemOut(BaseModel):
    kind: Literal["fact", "lesson"]
    content: str


class ReflectionOut(BaseModel):
    items: list[MemoryItemOut] = Field(default_factory=list)
