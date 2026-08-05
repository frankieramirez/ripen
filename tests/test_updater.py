from __future__ import annotations

from dataclasses import dataclass, replace
from datetime import UTC, datetime, timedelta

import pytest
import yaml

from nas_stack_updater.adapters import parse_image_reference
from nas_stack_updater.models import (
    CandidateObservation,
    GitHubPolicy,
    GitProposalResult,
    HealthPolicy,
    Mode,
    PendingProposal,
    Policy,
    PortainerStack,
    ResultCode,
    ServicePolicy,
    StackPolicy,
    UpdaterStatus,
)
from nas_stack_updater.updater import Updater


COMPOSE = """services:
  example-app:
    image: ghcr.io/example/example-app:latest
    restart: unless-stopped
"""
MULTI_COMPOSE = """services:
  radarr:
    image: lscr.io/linuxserver/radarr:latest
  sonarr:
    image: lscr.io/linuxserver/sonarr:latest
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
        self.service_digests = {
            "example-app": OLD,
            "radarr": OLD,
            "sonarr": OLD,
        }
        self.simulate_service_updates = False

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

    def get_service_image_digests(self, stack: PortainerStack) -> dict[str, str]:
        return dict(self.service_digests)

    def update_stack(
        self,
        stack: PortainerStack,
        compose: str,
        env: tuple[dict[str, str], ...],
        *,
        repull: bool,
    ) -> None:
        self.updates.append((compose, repull))
        if self.simulate_service_updates:
            parsed = yaml.safe_load(compose)
            for name, service in parsed["services"].items():
                image = service.get("image", "")
                if "@sha256:" in image:
                    self.service_digests[name] = image.rsplit("@", 1)[1]
            self.compose_values = [compose]
        if self.update_error is not None:
            if self.status_after_update_error is not None:
                self.image_status = self.status_after_update_error
            raise self.update_error


@dataclass
class FakeRegistry:
    digest: str = OLD
    platform_digests: dict[str, str] | None = None

    def resolve_digest(self, image: object) -> str:
        return self.digest

    def resolve_platform_digest(
        self,
        image: object,
        *,
        os_name: str,
        architecture: str,
        variant: str | None = None,
    ) -> str:
        repository = getattr(image, "repository")
        if self.platform_digests is not None:
            return self.platform_digests[repository]
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


class FakeGitProposals:
    def __init__(self) -> None:
        self.changes = []

    def propose(self, change):  # noqa: ANN001
        self.changes.append(change)
        return GitProposalResult("https://github.com/example/nas/pull/42", True)


class FailingNotifier:
    def emit(self, event: str, fields: dict[str, object]) -> None:
        raise RuntimeError("notification transport unavailable")


class SelectiveHealth:
    def __init__(self, unhealthy_target: str | None = None) -> None:
        self.unhealthy_target = unhealthy_target
        self.targets: list[str] = []

    def check(self, policy: HealthPolicy) -> bool:
        self.targets.append(policy.target)
        return policy.target != self.unhealthy_target


class FakeState:
    def __init__(self) -> None:
        self.accepted: dict[str, str] = {}
        self.candidates: dict[tuple[str, str], CandidateObservation] = {}
        self.breaker_reason: str | None = None
        self.leased = False
        self.attempts: list[tuple[str, str]] = []
        self.pending: dict[str, PendingProposal] = {}

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
            pending_proposals={
                stack: {"digest": item.digest, "url": item.url}
                for stack, item in self.pending.items()
            },
        )

    def get_accepted_digest(self, stack: str) -> str | None:
        return self.accepted.get(stack)

    def set_accepted_digest(self, stack: str, digest: str, now: datetime) -> None:
        self.accepted[stack] = digest
        self.pending.pop(stack, None)
        for key in [key for key in self.candidates if key[0] == stack]:
            del self.candidates[key]

    def get_pending_proposal(self, stack: str) -> PendingProposal | None:
        return self.pending.get(stack)

    def set_pending_proposal(
        self, stack: str, digest: str, url: str, now: datetime
    ) -> None:
        self.pending[stack] = PendingProposal(digest, url, now)

    def clear_pending_proposal(self, stack: str) -> bool:
        return self.pending.pop(stack, None) is not None

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


def make_multi_updater(
    *,
    auto_apply: bool = False,
    sonarr_auto_apply: bool = False,
    sonarr_enabled: bool = True,
    health_outcomes: list[bool] | None = None,
) -> tuple[
    Updater, FakePortainer, FakeRegistry, FakeState, FakeClock, FakeHealth
]:
    portainer = FakePortainer()
    portainer.stack = PortainerStack(211, 2, "arr", 1, ())
    portainer.visible = (portainer.stack,)
    portainer.compose_values = [MULTI_COMPOSE]
    portainer.service_digests = {"radarr": OLD, "sonarr": OLD}
    portainer.simulate_service_updates = True
    registry = FakeRegistry(
        platform_digests={
            "linuxserver/radarr": OLD,
            "linuxserver/sonarr": OLD,
        }
    )
    state = FakeState()
    clock = FakeClock()
    health = FakeHealth(health_outcomes)
    multi_policy = policy()
    multi_policy = Policy(
        **{
            **multi_policy.__dict__,
            "stacks": (
                StackPolicy(
                    "arr",
                    True,
                    False,
                    ("radarr", "sonarr"),
                    None,
                    (
                        ServicePolicy(
                            "radarr",
                            auto_apply,
                            HealthPolicy("http", "http://radarr:7878/", (200,), 1),
                        ),
                        ServicePolicy(
                            "sonarr",
                            sonarr_auto_apply,
                            HealthPolicy("http", "http://sonarr:8989/", (200,), 1),
                            enabled=sonarr_enabled,
                        ),
                    ),
                ),
            ),
            "excluded_stacks": frozenset(),
        }
    )
    updater = Updater(
        multi_policy,
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


def test_monitor_baselines_each_service_in_a_multi_service_stack() -> None:
    updater, portainer, _, state, _, _ = make_multi_updater()

    report = updater.run(Mode.MONITOR)

    assert [(item.stack, item.code) for item in report.results] == [
        ("arr/radarr", ResultCode.BASELINED),
        ("arr/sonarr", ResultCode.BASELINED),
    ]
    assert state.accepted == {
        "arr/radarr": OLD,
        "arr/sonarr": OLD,
    }
    assert portainer.updates == []


def test_monitor_skips_registry_resolution_for_health_only_service() -> None:
    updater, portainer, registry, state, _, _ = make_multi_updater(
        sonarr_enabled=False
    )
    assert registry.platform_digests is not None
    del registry.platform_digests["linuxserver/sonarr"]

    report = updater.run(Mode.MONITOR)

    assert [(item.stack, item.code) for item in report.results] == [
        ("arr/radarr", ResultCode.BASELINED),
    ]
    assert state.accepted == {"arr/radarr": OLD}
    assert portainer.updates == []


def test_health_only_service_still_blocks_sibling_update_when_unhealthy() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True, sonarr_enabled=False
    )
    updater.health = SelectiveHealth("http://sonarr:8989/")
    state.accepted = {"arr/radarr": OLD}
    assert registry.platform_digests is not None
    del registry.platform_digests["linuxserver/sonarr"]
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.results[0].code is ResultCode.INELIGIBLE
    assert "health preflight" in report.results[0].detail
    assert portainer.updates == []


def test_apply_updates_only_one_service_in_a_multi_service_stack() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    first = updater.run(Mode.APPLY)
    clock.advance(86400)
    second = updater.run(Mode.APPLY)

    assert [(item.stack, item.code) for item in first.results] == [
        ("arr/radarr", ResultCode.CANDIDATE),
        ("arr/sonarr", ResultCode.UP_TO_DATE),
    ]
    assert [(item.stack, item.code) for item in second.results] == [
        ("arr/radarr", ResultCode.UPDATED),
        ("arr/sonarr", ResultCode.UP_TO_DATE),
    ]
    assert len(portainer.updates) == 1
    deployed = yaml.safe_load(portainer.updates[0][0])
    assert deployed["services"]["radarr"]["image"].endswith("@" + NEW)
    assert deployed["services"]["sonarr"]["image"] == (
        "lscr.io/linuxserver/sonarr:latest"
    )
    assert portainer.updates[0][1] is False
    assert portainer.service_digests == {"radarr": NEW, "sonarr": OLD}

    follow_up = updater.run(Mode.MONITOR)
    assert [(item.stack, item.code) for item in follow_up.results] == [
        ("arr/radarr", ResultCode.UP_TO_DATE),
        ("arr/sonarr", ResultCode.UP_TO_DATE),
    ]


def test_apply_refuses_to_detach_git_backed_multi_service_stack() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    portainer.stack = PortainerStack(211, 2, "arr", 1, (), git_backed=True)
    portainer.visible = (portainer.stack,)
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.results[0].code is ResultCode.INELIGIBLE
    assert "Git proposal configuration" in report.results[0].detail
    assert portainer.updates == []
    assert state.breaker_reason is None


def test_git_backed_stack_creates_proposal_without_redeploying() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    portainer.stack = replace(portainer.stack, git_backed=True)
    portainer.visible = (portainer.stack,)
    updater.policy = replace(
        updater.policy,
        github=GitHubPolicy("example/nas", "main", "/secret"),
        stacks=(replace(updater.policy.stacks[0], git_path="stacks/arr/compose.yaml"),),
    )
    proposals = FakeGitProposals()
    updater.git_proposals = proposals
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.results[0].code is ResultCode.PROPOSED
    assert report.updates_applied == 0
    assert portainer.updates == []
    assert len(proposals.changes) == 1
    change = proposals.changes[0]
    assert change.repository_path == "stacks/arr/compose.yaml"
    assert f"lscr.io/linuxserver/radarr:latest@{NEW}" in change.proposed_content
    assert state.pending["arr/radarr"].digest == NEW


def test_git_deployment_is_accepted_only_after_digest_pin_and_health_match() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    portainer.stack = replace(portainer.stack, git_backed=True)
    portainer.visible = (portainer.stack,)
    updater.policy = replace(
        updater.policy,
        github=GitHubPolicy("example/nas", "main", "/secret"),
        stacks=(replace(updater.policy.stacks[0], git_path="stacks/arr/compose.yaml"),),
    )
    updater.git_proposals = FakeGitProposals()
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW
    updater.run(Mode.APPLY)
    clock.advance(86400)
    updater.run(Mode.APPLY)

    portainer.compose_values = [
        MULTI_COMPOSE.replace(
            "lscr.io/linuxserver/radarr:latest",
            f"lscr.io/linuxserver/radarr:latest@{NEW}",
        )
    ]
    portainer.service_digests["radarr"] = NEW
    report = updater.run(Mode.MONITOR)

    assert report.results[0].code is ResultCode.UPDATED
    assert report.updates_applied == 0
    assert state.accepted["arr/radarr"] == NEW
    assert "arr/radarr" not in state.pending
    assert portainer.updates == []


def test_unhealthy_git_deployment_opens_breaker_without_accepting_digest() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    portainer.stack = replace(portainer.stack, git_backed=True)
    portainer.visible = (portainer.stack,)
    updater.policy = replace(
        updater.policy,
        github=GitHubPolicy("example/nas", "main", "/secret"),
        stacks=(replace(updater.policy.stacks[0], git_path="stacks/arr/compose.yaml"),),
    )
    updater.git_proposals = FakeGitProposals()
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW
    updater.run(Mode.APPLY)
    clock.advance(86400)
    updater.run(Mode.APPLY)
    portainer.compose_values = [
        MULTI_COMPOSE.replace(
            "lscr.io/linuxserver/radarr:latest",
            f"lscr.io/linuxserver/radarr:latest@{NEW}",
        )
    ]
    portainer.service_digests["radarr"] = NEW
    updater.health = SelectiveHealth("http://radarr:7878/")

    report = updater.run(Mode.MONITOR)

    assert report.results[0].code is ResultCode.ERROR
    assert state.accepted["arr/radarr"] == OLD
    assert state.breaker_reason is not None


def test_operator_can_clear_reviewed_stale_proposal() -> None:
    updater, _, _, state, clock, _ = make_updater()
    state.set_pending_proposal(
        "example-app", NEW, "https://github.com/example/nas/pull/42", clock.now()
    )

    status = updater.clear_proposal("example-app", "PR closed without merge")

    assert status.pending_proposals == {}


def test_multi_service_update_preserves_compose_comments_and_anchors() -> None:
    compose = """# retained header
x-restart: &restart unless-stopped
services:
  radarr:
    image: lscr.io/linuxserver/radarr:latest # retain target comment
    restart: *restart
  sonarr:
    image: lscr.io/linuxserver/sonarr:latest
    restart: *restart
"""
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    portainer.compose_values = [compose]
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    updater.run(Mode.APPLY)

    deployed = portainer.updates[0][0]
    assert "# retained header" in deployed
    assert "# retain target comment" in deployed
    assert "&restart" in deployed
    assert deployed.count("*restart") == 2
    assert f'"lscr.io/linuxserver/radarr:latest@{NEW}"' in deployed


def test_failed_multi_service_health_check_rolls_back_only_changed_service() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True,
        health_outcomes=[True, True, False, False, False, True, True],
    )
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert [(item.stack, item.code) for item in report.results] == [
        ("arr/radarr", ResultCode.ROLLED_BACK),
        ("arr/sonarr", ResultCode.UP_TO_DATE),
    ]
    assert len(portainer.updates) == 2
    deployed = [yaml.safe_load(item[0]) for item in portainer.updates]
    assert deployed[0]["services"]["radarr"]["image"].endswith("@" + NEW)
    assert deployed[1]["services"]["radarr"]["image"].endswith("@" + OLD)
    assert all(
        item["services"]["sonarr"]["image"]
        == "lscr.io/linuxserver/sonarr:latest"
        for item in deployed
    )
    assert all(repull is False for _, repull in portainer.updates)
    assert portainer.service_digests == {"radarr": OLD, "sonarr": OLD}
    assert state.breaker_reason is not None
    assert "arr/radarr" in state.breaker_reason


def test_apply_changes_at_most_one_service_when_two_candidates_are_mature() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True, sonarr_auto_apply=True
    )
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests = {
        "linuxserver/radarr": NEW,
        "linuxserver/sonarr": NEW,
    }

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.updates_applied == 1
    assert [(item.stack, item.code) for item in report.results] == [
        ("arr/radarr", ResultCode.UPDATED),
        ("arr/sonarr", ResultCode.CANDIDATE),
    ]
    assert portainer.service_digests == {"radarr": NEW, "sonarr": OLD}


def test_apply_refuses_to_mutate_when_another_stack_service_is_unhealthy() -> None:
    updater, portainer, registry, state, clock, _ = make_multi_updater(
        auto_apply=True
    )
    updater.health = SelectiveHealth("http://sonarr:8989/")
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.results[0].stack == "arr/radarr"
    assert report.results[0].code is ResultCode.INELIGIBLE
    assert "health preflight" in report.results[0].detail
    assert portainer.updates == []


def test_apply_verifies_every_stack_service_before_and_after_update() -> None:
    updater, _, registry, state, clock, _ = make_multi_updater(auto_apply=True)
    health = SelectiveHealth()
    updater.health = health
    state.accepted = {"arr/radarr": OLD, "arr/sonarr": OLD}
    assert registry.platform_digests is not None
    registry.platform_digests["linuxserver/radarr"] = NEW

    updater.run(Mode.APPLY)
    clock.advance(86400)
    report = updater.run(Mode.APPLY)

    assert report.results[0].code is ResultCode.UPDATED
    assert health.targets.count("http://radarr:7878/") >= 2
    assert health.targets.count("http://sonarr:8989/") >= 2


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
    assert f"ghcr.io/example/example-app:latest@{OLD}" in portainer.updates[1][0]
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


def test_health_adapter_exception_times_out_into_rollback() -> None:
    class ExplodingHealth:
        def check(self, policy: HealthPolicy) -> bool:
            raise RuntimeError("temporary health transport failure")

    updater, portainer, registry, state, clock, _ = make_updater(auto_apply=True)
    updater.health = ExplodingHealth()
    state.accepted["example-app"] = OLD
    portainer.image_status = "outdated"
    registry.digest = NEW

    assert result_code(updater, Mode.APPLY) is ResultCode.CANDIDATE
    clock.advance(86400)

    assert result_code(updater, Mode.APPLY) is ResultCode.ROLLBACK_FAILED
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


def test_tagged_digest_reference_preserves_update_channel_and_pin() -> None:
    image = parse_image_reference(f"ghcr.io/example/app:latest@{OLD}")

    assert image.tagged == "ghcr.io/example/app:latest"
    assert image.pinned_digest == OLD
    assert image.pinned(NEW) == f"ghcr.io/example/app:latest@{NEW}"


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
