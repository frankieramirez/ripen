from __future__ import annotations

from datetime import datetime
from typing import Protocol

from .models import (
    CandidateObservation,
    HealthPolicy,
    ImageReference,
    PortainerStack,
    UpdaterStatus,
)


class PortainerPort(Protocol):
    def current_username(self) -> str: ...

    def list_stacks(self) -> tuple[PortainerStack, ...]: ...

    def get_stack_file(self, stack_id: int) -> str: ...

    def get_image_status(self, stack_id: int) -> str: ...

    def get_service_image_digests(self, stack: PortainerStack) -> dict[str, str]: ...

    def update_stack(
        self,
        stack: PortainerStack,
        compose: str,
        env: tuple[dict[str, str], ...],
        *,
        repull: bool,
    ) -> None: ...


class RegistryPort(Protocol):
    def resolve_digest(self, image: ImageReference) -> str: ...

    def resolve_platform_digest(
        self,
        image: ImageReference,
        *,
        os_name: str,
        architecture: str,
        variant: str | None = None,
    ) -> str: ...


class HealthPort(Protocol):
    def check(self, policy: HealthPolicy) -> bool: ...


class StateStore(Protocol):
    def acquire_lease(self, now: datetime, ttl_seconds: int) -> str | None: ...

    def release_lease(self, owner_token: str) -> None: ...

    def get_status(self, now: datetime) -> UpdaterStatus: ...

    def get_accepted_digest(self, stack: str) -> str | None: ...

    def set_accepted_digest(self, stack: str, digest: str, now: datetime) -> None: ...

    def observe_candidate(
        self, stack: str, digest: str, now: datetime
    ) -> CandidateObservation: ...

    def record_attempt(
        self,
        stack: str,
        old_digest: str,
        new_digest: str,
        result: str,
        now: datetime,
        detail: str,
    ) -> None: ...

    def open_breaker(self, reason: str, now: datetime) -> None: ...

    def clear_breaker(self, reason: str, now: datetime) -> None: ...


class NotifierPort(Protocol):
    def emit(self, event: str, fields: dict[str, object]) -> None: ...


class Clock(Protocol):
    def now(self) -> datetime: ...

    def sleep(self, seconds: float) -> None: ...
