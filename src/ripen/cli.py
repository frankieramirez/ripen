from __future__ import annotations

import argparse
import json
import sqlite3
import sys

from .adapters import (
    AdapterError,
    FunctionalHealthAdapter,
    GitHubProposalAdapter,
    JsonLogNotifier,
    OciRegistryAdapter,
    PortainerHttpAdapter,
    SqliteStateStore,
    SystemClock,
)
from .config import ConfigError, load_policy
from .models import Mode
from .updater import Updater


def _report_exit_code(report: object) -> int:
    results = getattr(report, "results", ())
    return int(
        any(result.code.value in {"error", "rollback_failed"} for result in results)
    )


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="ripen")
    parser.add_argument("--config", default="/config/policy.yaml")
    commands = parser.add_subparsers(dest="command", required=True)

    run = commands.add_parser("run", help="execute one monitor or apply cycle")
    run.add_argument("--mode", choices=[item.value for item in Mode])

    daemon = commands.add_parser("daemon", help="run monitor cycles on an interval")
    daemon.add_argument("--mode", choices=[item.value for item in Mode])
    daemon.add_argument("--once", action="store_true")

    commands.add_parser("status", help="show breaker, lease, and accepted digests")

    clear = commands.add_parser("clear-breaker", help="clear a reviewed circuit breaker")
    clear.add_argument("--reason", required=True)
    proposal = commands.add_parser(
        "clear-proposal", help="clear a reviewed stale Git proposal record"
    )
    proposal.add_argument("--stack", required=True)
    proposal.add_argument("--reason", required=True)
    return parser


def _build(config_path: str) -> tuple[Updater, SystemClock]:
    policy = load_policy(config_path)
    clock = SystemClock()
    updater = Updater(
        policy,
        portainer=PortainerHttpAdapter(
            policy.portainer_base_url,
            policy.portainer_api_key_file,
            ca_file=policy.tls_ca_file,
            fingerprint_sha256=policy.tls_fingerprint_sha256,
        ),
        registry=OciRegistryAdapter(),
        health=FunctionalHealthAdapter(),
        state=SqliteStateStore(policy.state_file),
        notifier=JsonLogNotifier(),
        clock=clock,
        git_proposals=(
            GitHubProposalAdapter(
                policy.github.repository,
                policy.github.base_branch,
                policy.github.token_file,
            )
            if policy.github is not None
            else None
        ),
    )
    return updater, clock


def main(argv: list[str] | None = None) -> int:
    args = _parser().parse_args(argv)
    try:
        updater, clock = _build(args.config)
        if args.command == "run":
            report = updater.run(Mode(args.mode) if args.mode else None)
            print(json.dumps(report.to_dict(), indent=2, sort_keys=True))
            return _report_exit_code(report)
        if args.command == "status":
            print(json.dumps(updater.status().to_dict(), indent=2, sort_keys=True))
            return 0
        if args.command == "clear-breaker":
            print(
                json.dumps(
                    updater.clear_breaker(args.reason).to_dict(), indent=2, sort_keys=True
                )
            )
            return 0
        if args.command == "clear-proposal":
            print(
                json.dumps(
                    updater.clear_proposal(args.stack, args.reason).to_dict(),
                    indent=2,
                    sort_keys=True,
                )
            )
            return 0
        selected_mode = Mode(args.mode) if args.mode else None
        while True:
            try:
                report = updater.run(selected_mode)
            except (AdapterError, sqlite3.Error) as error:
                print(
                    json.dumps({"event": "run_error", "error": str(error)}),
                    file=sys.stderr,
                    flush=True,
                )
                if args.once:
                    return 1
                clock.sleep(updater.policy.check_interval_seconds)
                continue
            print(json.dumps(report.to_dict(), sort_keys=True), flush=True)
            if args.once:
                return _report_exit_code(report)
            clock.sleep(updater.policy.check_interval_seconds)
    except (ConfigError, OSError, ValueError, PermissionError) as error:
        print(f"configuration error: {error}", file=sys.stderr)
        return 2
    except (AdapterError, sqlite3.Error) as error:
        print(f"operational error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
