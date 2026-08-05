from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from nas_stack_updater.adapters import SqliteStateStore


NOW = datetime(2026, 8, 1, tzinfo=UTC)
OLD = "sha256:" + "1" * 64
NEW = "sha256:" + "2" * 64


def store(tmp_path: Path) -> SqliteStateStore:
    return SqliteStateStore(tmp_path / "state" / "updater.db")


def test_state_persists_baseline_and_candidate_observations(tmp_path: Path) -> None:
    first = store(tmp_path)
    first.set_accepted_digest("example-app", OLD, NOW)
    observation = first.observe_candidate("example-app", NEW, NOW)
    second = first.observe_candidate("example-app", NEW, NOW + timedelta(days=1))

    assert observation.count == 1
    assert second.count == 2
    assert second.first_seen == NOW
    assert store(tmp_path).get_accepted_digest("example-app") == OLD


def test_state_persists_independent_service_digests_for_one_stack(tmp_path: Path) -> None:
    state = store(tmp_path)

    state.set_accepted_digest("arr/radarr", OLD, NOW)
    state.set_accepted_digest("arr/sonarr", NEW, NOW)

    reopened = store(tmp_path)
    assert reopened.get_accepted_digest("arr/radarr") == OLD
    assert reopened.get_accepted_digest("arr/sonarr") == NEW
    assert reopened.get_status(NOW).accepted_digests == {
        "arr/radarr": OLD,
        "arr/sonarr": NEW,
    }


def test_state_persists_and_clears_pending_git_proposal(tmp_path: Path) -> None:
    state = store(tmp_path)
    state.set_accepted_digest("arr/radarr", OLD, NOW)
    state.set_pending_proposal(
        "arr/radarr", NEW, "https://github.com/example/nas/pull/42", NOW
    )

    pending = store(tmp_path).get_pending_proposal("arr/radarr")
    assert pending is not None
    assert pending.digest == NEW
    assert pending.url.endswith("/pull/42")
    assert store(tmp_path).get_status(NOW).pending_proposals == {
        "arr/radarr": {"digest": NEW, "url": pending.url}
    }

    state.set_accepted_digest("arr/radarr", NEW, NOW + timedelta(minutes=1))
    assert state.get_pending_proposal("arr/radarr") is None


def test_clear_pending_proposal_reports_whether_record_existed(tmp_path: Path) -> None:
    state = store(tmp_path)
    assert state.clear_pending_proposal("arr/radarr") is False
    state.set_pending_proposal(
        "arr/radarr", NEW, "https://github.com/example/nas/pull/42", NOW
    )
    assert state.clear_pending_proposal("arr/radarr") is True
    assert state.clear_pending_proposal("arr/radarr") is False


def test_lease_excludes_concurrent_run_and_expires(tmp_path: Path) -> None:
    state = store(tmp_path)

    first_token = state.acquire_lease(NOW, 60)
    assert first_token is not None
    assert state.acquire_lease(NOW + timedelta(seconds=30), 60) is None
    second_token = state.acquire_lease(NOW + timedelta(seconds=61), 60)
    assert second_token is not None
    assert state.get_status(NOW + timedelta(seconds=70)).lease_active is True
    state.release_lease(first_token)
    assert state.get_status(NOW + timedelta(seconds=70)).lease_active is True
    state.release_lease(second_token)
    assert state.get_status(NOW + timedelta(seconds=70)).lease_active is False


def test_breaker_requires_explicit_clear_reason(tmp_path: Path) -> None:
    state = store(tmp_path)
    state.open_breaker("rollback failed", NOW)

    assert state.get_status(NOW).breaker_open is True
    with pytest.raises(ValueError, match="reason is required"):
        state.clear_breaker("   ", NOW)
    assert state.get_status(NOW).breaker_open is True
    state.clear_breaker("manually verified example-app", NOW + timedelta(minutes=1))
    assert state.get_status(NOW).breaker_open is False
