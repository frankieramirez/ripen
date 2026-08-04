from __future__ import annotations

from dataclasses import asdict, dataclass, field
from datetime import datetime
from enum import StrEnum
from typing import Any


class Mode(StrEnum):
    MONITOR = "monitor"
    APPLY = "apply"


class ResultCode(StrEnum):
    BASELINED = "baselined"
    BASELINE_BLOCKED = "baseline_blocked"
    BREAKER_OPEN = "breaker_open"
    BUSY = "busy"
    CANDIDATE = "candidate"
    DRIFTED = "drifted"
    ERROR = "error"
    EXCLUDED = "excluded"
    INELIGIBLE = "ineligible"
    NOT_VISIBLE = "not_visible"
    ROLLBACK_FAILED = "rollback_failed"
    ROLLED_BACK = "rolled_back"
    UPDATED = "updated"
    UP_TO_DATE = "up_to_date"


@dataclass(frozen=True)
class HealthPolicy:
    type: str
    target: str
    accepted_status: tuple[int, ...] = (200,)
    timeout_seconds: float = 5.0


@dataclass(frozen=True)
class ServicePolicy:
    name: str
    auto_apply: bool
    health: HealthPolicy
    enabled: bool = True


@dataclass(frozen=True)
class StackPolicy:
    name: str
    enabled: bool
    auto_apply: bool
    expected_services: tuple[str, ...]
    health: HealthPolicy | None
    services: tuple[ServicePolicy, ...] = ()


@dataclass(frozen=True)
class Policy:
    mode: Mode
    max_updates_per_run: int
    verification_timeout_seconds: int
    candidate_min_age_seconds: int
    lease_ttl_seconds: int
    portainer_base_url: str
    portainer_api_key_file: str
    expected_username: str
    tls_ca_file: str | None
    tls_fingerprint_sha256: str | None
    state_file: str
    check_interval_seconds: int
    stacks: tuple[StackPolicy, ...]
    excluded_stacks: frozenset[str]


@dataclass(frozen=True)
class PortainerStack:
    id: int
    endpoint_id: int
    name: str
    status: int
    env: tuple[dict[str, str], ...] = ()


@dataclass(frozen=True)
class ImageReference:
    registry: str
    repository: str
    tag: str
    original: str
    pinned_digest: str | None = None

    @property
    def tagged(self) -> str:
        return f"{self.registry}/{self.repository}:{self.tag}"

    def pinned(self, digest: str) -> str:
        return f"{self.tagged}@{digest}"


@dataclass(frozen=True)
class StackObservation:
    stack: PortainerStack
    compose: str
    compose_hash: str
    env_hash: str
    service_name: str
    image: ImageReference
    image_status: str
    remote_digest: str
    state_key: str
    health: HealthPolicy
    auto_apply: bool
    running_digest: str | None = None


@dataclass(frozen=True)
class StackResult:
    stack: str
    code: ResultCode
    detail: str
    digest: str | None = None


@dataclass(frozen=True)
class RunReport:
    mode: Mode
    started_at: datetime
    finished_at: datetime
    results: tuple[StackResult, ...]
    updates_applied: int = 0
    breaker_open: bool = False

    def to_dict(self) -> dict[str, Any]:
        value = asdict(self)
        value["mode"] = self.mode.value
        value["started_at"] = self.started_at.isoformat()
        value["finished_at"] = self.finished_at.isoformat()
        for result in value["results"]:
            result["code"] = result["code"].value
        return value


@dataclass(frozen=True)
class UpdaterStatus:
    breaker_open: bool
    breaker_reason: str | None
    accepted_digests: dict[str, str] = field(default_factory=dict)
    lease_active: bool = False

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass(frozen=True)
class CandidateObservation:
    digest: str
    first_seen: datetime
    last_seen: datetime
    count: int
