from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime, timedelta

import pytest

from nas_stack_updater.adapters import parse_image_reference
from nas_stack_updater.models import (
    CandidateObservation,
    HealthPolicy,
    Mode,
    Policy,
    PortainerStack,
    ResultCode,
    StackPolicy,
    UpdaterStatus,
)
from nas_stack_updater.updater import Updater


COMPOSE = """services:
  example-app:
    image: ghcr.io/example/example-app:latest
    restart: unless-stopped
"""
DRIFTED_COMPOSE = COMPOSE + "    environment:\n      CHANGED: 'true'\n"
OLD = "sha256:" + "1" * 64
NEW = "sha256:" + "2" * 64


class FakeClock:
    def __init__(self) -> None:
        self.value = datetime(2026, 8, 1, 8, tzinfo=UTC)

    def now(self) -> datetime:
        return self.value

    def sleep(self, seconds: float) -> None:
        self.value += timedelta(seconds=seconds)

    def advance(self, seconds: int) -> None:
        self.value += timedelta(seconds=seconds)


class FakePortainer:
    def __init__(self) -> None:
        self.username = "nas-stack-updater"
        self.stack = PortainerStack(147, 2, "example-app", 1, ())
        self.visible = (self.stack,)
        self.compose_values = [COMPOSE]
        self.image_status = "updated"
        self.updates: list[tuple[str, bool]] = []
        self.update_error: Exception | None = None
        self.status_after_update_error: str | None = None

    def current_username(self) -> str:
        return self.username

    def list_stacks(self) -> tuple[PortainerStack, ...]:
        return self.visible

    def get_stack_file(self, stack_id: int) -> str:
        if len(self.compose_values) > 1:
            return self.compose_values.pop(0)
        return self.compose_values[0]

    def get_image_status(self, stack_id: int) -> str:
        return self.image_status

    def update_stack(
        self,
        stack: PortainerStack,
        compose: str,
        env: tuple[dict[str, str], ...],
        *,
        repull: bool,
    ) -> None:
        self.updates.append((compose, repull))
        if self.update_error is not None:
            if self.status_after_update_error is not None:
                self.image_status = self.status_after_update_error
            raise self.update_error


@dataclass
class FakeRegistry:
    digest: str = OLD

    def resolve_digest(self, image: object) -> str:
        return self.digest


class FakeHealth:
    def __init__(self, outcomes: list[bool] | None = None) -> None:
        self.outcomes = outcomes or [True]
        self.calls = 0

    def check(self, policy: HealthPolicy) -> bool:
        index = min(self.calls, len(self.outcomes) - 1)
        self.calls += 1
        return self.outcomes[index]


class FakeNotifier:
    def __init__(self) -> None:
        self.events: list[tuple[str, dict[str, object]]] = []

    def emit(self, event: str, fields: dict[str, object]) -> None:
        self.events.append((event, fields))


class FailingNotifier:
    def emit(self, event: str, fields: dict[str, object]) -> None:
        raise RuntimeError("notification transport unavailable")


class FakeState:
    def __init__(self) -> None:
        self.accepted: dict[str, str] = {}
        self.candidates: dict[tuple[str, str], CandidateObservation] = {}
        self.breaker_reason: str | None = None
        self.leased = False
        self.attempts: list[tuple[str, str]] = []

    def acquire_lease(self, now: datetime, ttl_seconds: int) -> str | None:
        if self.leased:
            return None
        self.leased = True
        return "lease-token"

    def release_lease(self, owner_token: str) -> None:
        if owner_token == "lease-token":
            self.leased = False

    def get_status(self, now: datetime) -> UpdaterStatus:
        return UpdaterStatus(
            breaker_open=self.breaker_reason is not None,
            breaker_reason=self.breaker_reason,
            accepted_digests=dict(self.accepted),
            lease_active=self.leased,
        )

    def get_accepted_digest(self, stack: str) -> str | None:
        return self.accepted.get(stack)

    def set_accepted_digest(self, stack: str, digest: str, now: datetime) -> None:
        self.accepted[stack] = digest
        for key in [key for key in self.candidates if key[0] == stack]:
            del self.candidates[key]

    def observe_candidate(
        self, stack: str, digest: str, now: datetime
    ) -> CandidateObservation:
        key = (stack, digest)
        previous = self.candidates.get(key)
        observation = CandidateObservation(
            digest,
            previous.first_seen if previous else now,
            now,
            previous.count + 1 if previous else 1,
        )
        self.candidates[key] = observation
        return observation

    def record_attempt(
        self,
        stack: str,
        old_digest: str,
        new_digest: str,
        result: str,
        now: datetime,
        detail: str,
    ) -> None:
        self.attempts.append((stack, result))

    def open_breaker(self, reason: str, now: datetime) -> None:
        self.breaker_reason = reason

    def clear_breaker(self, reason: str, now: datetime) -> None:
        if not reason.strip():
            raise ValueError("reason required")
        self.breaker_reason = None


def policy(*, auto_apply: bool = False, timeout: int = 20) -> Policy:
    return Policy(
        mode=Mode.MONITOR,
        max_updates_per_run=1,
        verification_timeout_seconds=timeout,
        candidate_min_age_seconds=86400,
        lease_ttl_seconds=1800,
        portainer_base_url="https://portainer:9443",
        portainer_api_key_file="/secret",
        expected_username="nas-stack-updater",
        tls_ca_file=None,
        tls_fingerprint_sha256="a" * 64,
        state_file=":memory:",
        check_interval_seconds=86400,
        stacks=(
            StackPolicy(
                "example-app",
                True,
                auto_apply,
                ("example-app",),
                HealthPolicy("http", "http://nas:8091/", (200,), 1),
            ),
        ),
        excluded_stacks=frozenset({"arr", "download-clients-vpn"}),
    )


def make_updater(
    *, auto_apply: bool = False, health_outcomes: list[bool] | None = None
) -> tuple[Updater, FakePortainer, FakeRegistry, FakeState, FakeClock, FakeHealth]:
    portainer = FakePortainer()
    registry = FakeRegistry()
    state = FakeState()
    clock = FakeClock()
    health = FakeHealth(health_outcomes)
    updater = Updater(
        policy(auto_apply=auto_apply),
        portainer=portainer,
        registry=registry,
        health=health,
        state=state,
        notifier=FakeNotifier(),
        clock=clock,
    )
    return updater, portainer, registry, state, clock, health


def result_code(updater: Updater, mode: Mode | None = None) -> ResultCode:
    report = updater.run(mode)
    assert len(report.results) == 1
    return report.results[0].code


def test_monitor_records_proven_baseline_without_redeploying() -> None:
    updater, portainer, _, state, _, _ = make_updater()

    assert result_code(updater) is ResultCode.BASELINED
    assert state.accepted["example-app"] == OLD
    assert portainer.updates == []


def test_monitor_refuses_to_baseline_when_update_already_pending() -> None:
    updater, portainer, registry, state, _, _ = make_updater()
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater) is ResultCode.BASELINE_BLOCKED
    assert state.accepted == {}


def test_monitor_reports_candidate_without_redeploying() -> None:
    updater, portainer, registry, state, _, _ = make_updater()
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.MONITOR) is ResultCode.CANDIDATE
    assert portainer.updates == []


def test_apply_updates_mature_candidate_after_health_passes() -> None:
    updater, portainer, registry, state, clock, _ = make_updater(auto_apply=True)
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)
    assert result_code(updater, Mode.APPLY) is ResultCode.UPDATED
    assert state.accepted["example-app"] == NEW
    assert len(portainer.updates) == 1
    assert portainer.updates[0][1] is True


def test_timed_out_update_is_accepted_when_health_and_image_status_prove_success() -> None:
    updater, portainer, registry, state, clock, _ = make_updater(auto_apply=True)
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    portainer.update_error = TimeoutError("The read operation timed out")
    portainer.status_after_update_error = "updated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)
    assert result_code(updater, Mode.APPLY) is ResultCode.UPDATED
    assert state.accepted["example-app"] == NEW
    assert len(portainer.updates) == 1
    assert state.breaker_reason is None


def test_failed_health_rolls_back_to_digest_and_opens_breaker() -> None:
    updater, portainer, registry, state, clock, _ = make_updater(
        auto_apply=True, health_outcomes=[False, False, False, True]
    )
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)
    assert result_code(updater, Mode.APPLY) is ResultCode.ROLLED_BACK
    assert len(portainer.updates) == 2
    assert f"ghcr.io/example/example-app@{OLD}" in portainer.updates[1][0]
    assert state.breaker_reason is not None


def test_failed_rollback_health_opens_breaker_and_stops_future_apply() -> None:
    updater, portainer, registry, state, clock, _ = make_updater(
        auto_apply=True, health_outcomes=[False]
    )
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)
    assert result_code(updater, Mode.APPLY) is ResultCode.ROLLBACK_FAILED
    assert result_code(updater, Mode.APPLY) is ResultCode.BREAKER_OPEN
    assert len(portainer.updates) == 2


def test_apply_cancels_when_compose_drifts_after_planning() -> None:
    updater, portainer, registry, state, clock, _ = make_updater(auto_apply=True)
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)
    portainer.compose_values = [COMPOSE, DRIFTED_COMPOSE]
    assert result_code(updater, Mode.APPLY) is ResultCode.DRIFTED
    assert portainer.updates == []


def test_wrong_portainer_identity_fails_before_inventory() -> None:
    updater, portainer, _, _, _, _ = make_updater()
    portainer.username = "administrator"

    with pytest.raises(PermissionError):
        updater.run()


def test_image_parser_normalizes_docker_hub_and_preserves_ghcr() -> None:
    docker = parse_image_reference("nginx:stable")
    ghcr = parse_image_reference("ghcr.io/example/app:latest")

    assert docker.registry == "registry-1.docker.io"
    assert docker.repository == "library/nginx"
    assert docker.tag == "stable"
    assert ghcr.registry == "ghcr.io"
    assert ghcr.repository == "example/app"


def test_digest_pinned_image_is_ineligible() -> None:
    with pytest.raises(ValueError, match="mutable tagged"):
        parse_image_reference(f"ghcr.io/example/app@{OLD}")


@pytest.mark.parametrize(
    "image",
    (
        "ghcr.io/example/../../escape:latest",
        "ghcr.io/example/app:bad/tag",
        "ghcr.io/Example/app:latest",
    ),
)
def test_invalid_image_reference_is_rejected(image: str) -> None:
    with pytest.raises(ValueError, match="valid OCI"):
        parse_image_reference(image)


def test_environment_hash_is_independent_of_portainer_order() -> None:
    first = ({"name": "A", "value": "1"}, {"name": "B", "value": "2"})
    second = tuple(reversed(first))

    assert Updater._env_hash(first) == Updater._env_hash(second)


def test_notification_failure_does_not_discard_run_result() -> None:
    updater, _, _, _, _, _ = make_updater()
    updater.notifier = FailingNotifier()

    assert result_code(updater) is ResultCode.BASELINED
