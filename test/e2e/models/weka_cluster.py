from enum import Enum
from typing import Literal

import pydantic

from .kubernetes import ApiObject, Condition


class Spec(pydantic.BaseModel):
    """Placeholder for WekaCluster and WekaContainer spec"""

    pass


class WekaClusterStatus(pydantic.BaseModel):
    conditions: list[Condition]


class WekaCluster(ApiObject):
    apiVersion: str = "weka.weka.io/v1alpha1"
    kind: str = "WekaCluster"
    spec: Spec  # Placeholder for WekaCluster spec
    status: WekaClusterStatus = pydantic.Field(default=None)


class WekaContainerSpec(pydantic.BaseModel):
    mode: Literal["drive", "compute", "dist", "builder"]


class WekaContainerStatus(pydantic.BaseModel):
    conditions: list[Condition]


class WekaContainer(ApiObject):
    apiVersion: str = "weka.weka.io/v1alpha1"
    kind: str = "WekaContainer"
    spec: WekaContainerSpec
    status: WekaContainerStatus = pydantic.Field(default=None)


class WekaContainerList(pydantic.BaseModel):
    items: list[WekaContainer]
