from typing import Optional

from kubernetes.client import ApiextensionsV1Api
from pydantic import BaseModel, Field


class OwnerReference(BaseModel):
    apiVersion: str
    kind: str
    name: str
    uid: str
    controller: bool = True
    blockOwnerDeletion: bool = True


class Metadata(BaseModel):
    name: str
    namespace: str = "default"
    labels: dict = Field(default_factory=dict)
    annotations: dict = Field(default_factory=dict)
    uid: Optional[str] = None
    ownerReferences: list[OwnerReference] = Field(default_factory=list)


class Condition(BaseModel):
    type: str
    status: str


class ApiObject(BaseModel):
    apiVersion: str
    kind: str
    metadata: Metadata


class Namespace(ApiObject):
    apiVersion: str = "v1"
    kind: str = "Namespace"


class Secret(ApiObject):
    apiVersion: str = "v1"
    kind: str = "Secret"
    type: str
    data: dict = Field(default_factory=dict)
