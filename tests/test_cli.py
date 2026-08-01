from types import SimpleNamespace

from nas_stack_updater import cli
from nas_stack_updater.adapters import AdapterError


class FailingUpdater:
    policy = SimpleNamespace(check_interval_seconds=60)

    def run(self, mode=None):  # noqa: ANN001
        raise AdapterError("Portainer temporarily unavailable")


class FakeClock:
    def sleep(self, seconds: float) -> None:
        raise AssertionError("--once must not sleep")


def test_run_reports_operational_error_without_traceback(monkeypatch, capsys) -> None:
    monkeypatch.setattr(cli, "_build", lambda path: (FailingUpdater(), FakeClock()))

    assert cli.main(["--config", "unused", "run"]) == 1
    assert "operational error: Portainer temporarily unavailable" in capsys.readouterr().err


def test_daemon_once_reports_transient_error_without_sleep(monkeypatch, capsys) -> None:
    monkeypatch.setattr(cli, "_build", lambda path: (FailingUpdater(), FakeClock()))

    assert cli.main(["--config", "unused", "daemon", "--once"]) == 1
    assert '"event": "run_error"' in capsys.readouterr().err
