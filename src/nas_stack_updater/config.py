from __future__ import annotations

from pathlib import Path
import string
from typing import Any
import urllib.parse

import yaml

from .models import HealthPolicy, Mode, Policy, ServicePolicy, StackPolicy


class ConfigError(ValueError):
    pass


def _mapping(value: Any, path: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise ConfigError(f"{path} must be a mapping")
    return value


def _exact_keys(value: dict[str, Any], allowed: set[str], path: str) -> None:
    unknown = sorted(set(value) - allowed)
    if unknown:
        raise ConfigError(f"unknown {path} fields: {', '.join(unknown)}")


def _required(value: dict[str, Any], key: str, path: str) -> Any:
    if key not in value:
        raise ConfigError(f"{path}.{key} is required")
    return value[key]


def _positive_int(value: Any, path: str) -> int:
    try:
        parsed = int(value)
    except (TypeError, ValueError) as error:
        raise ConfigError(f"{path} must be an integer") from error
    if parsed <= 0:
        raise ConfigError(f"{path} must be greater than zero")
    return parsed


def _health_policy(raw_value: Any, path: str) -> HealthPolicy:
    raw = _mapping(raw_value, path)
    _exact_keys(
        raw,
        {"type", "url", "target", "accepted_status", "timeout_seconds"},
        path,
    )
    target = raw.get("target", raw.get("url"))
    if not isinstance(target, str) or not target:
        raise ConfigError(f"{path}.target or url is required")
    statuses = raw.get("accepted_status", [200])
    if (
        not isinstance(statuses, list)
        or not statuses
        or not all(
            isinstance(item, int)
            and not isinstance(item, bool)
            and 100 <= item <= 599
            for item in statuses
        )
    ):
        raise ConfigError(
            f"{path}.accepted_status must be a non-empty list of HTTP status codes"
        )
    return HealthPolicy(
        type=str(raw.get("type", "http")),
        target=target,
        accepted_status=tuple(statuses),
        timeout_seconds=float(raw.get("timeout_seconds", 5)),
    )


def load_policy(path: str | Path) -> Policy:
    source = Path(path)
    payload = yaml.safe_load(source.read_text(encoding="utf-8"))
    root = _mapping(payload, "config")
    _exact_keys(
        root,
        {
            "mode",
            "max_updates_per_run",
            "verification_timeout_seconds",
            "candidate_min_age_seconds",
            "lease_ttl_seconds",
            "check_interval_seconds",
            "portainer",
            "state_file",
            "stacks",
            "exclude",
        },
        "config",
    )

    portainer = _mapping(_required(root, "portainer", "config"), "portainer")
    _exact_keys(
        portainer,
        {
            "base_url",
            "api_key_file",
            "expected_username",
            "tls_ca_file",
            "tls_fingerprint_sha256",
        },
        "portainer",
    )

    stacks_raw = _mapping(_required(root, "stacks", "config"), "stacks")
    stacks: list[StackPolicy] = []
    for name, raw_value in stacks_raw.items():
        raw = _mapping(raw_value, f"stacks.{name}")
        _exact_keys(
            raw,
            {"enabled", "auto_apply", "expected_services", "health", "services"},
            f"stacks.{name}",
        )
        expected = raw.get("expected_services", [])
        if not isinstance(expected, list) or not expected or not all(
            isinstance(item, str) and item for item in expected
        ):
            raise ConfigError(f"stacks.{name}.expected_services must be a non-empty list")
        if len(set(expected)) != len(expected):
            raise ConfigError(
                f"stacks.{name}.expected_services must not contain duplicate names"
            )
        if "services" in raw and ({"auto_apply", "health"} & set(raw)):
            raise ConfigError(
                f"stacks.{name} cannot use stack-level auto_apply or health "
                "with per-service rules"
            )
        if len(expected) > 1 and "services" not in raw:
            raise ConfigError(
                f"stacks.{name} requires per-service rules for multiple services"
            )
        service_policies: tuple[ServicePolicy, ...] = ()
        health: HealthPolicy | None = None
        if "services" in raw:
            services_raw = _mapping(raw["services"], f"stacks.{name}.services")
            if set(services_raw) != set(expected):
                raise ConfigError(
                    f"stacks.{name}.services must exactly match expected_services"
                )
            parsed_services: list[ServicePolicy] = []
            for service_name in expected:
                service_raw = _mapping(
                    services_raw[service_name], f"stacks.{name}.services.{service_name}"
                )
                _exact_keys(
                    service_raw,
                    {"auto_apply", "health"},
                    f"stacks.{name}.services.{service_name}",
                )
                parsed_services.append(
                    ServicePolicy(
                        name=service_name,
                        auto_apply=bool(service_raw.get("auto_apply", False)),
                        health=_health_policy(
                            _required(
                                service_raw,
                                "health",
                                f"stacks.{name}.services.{service_name}",
                            ),
                            f"stacks.{name}.services.{service_name}.health",
                        ),
                    )
                )
            service_policies = tuple(parsed_services)
        else:
            health = _health_policy(
                _required(raw, "health", f"stacks.{name}"),
                f"stacks.{name}.health",
            )
        stacks.append(
            StackPolicy(
                name=str(name),
                enabled=bool(raw.get("enabled", False)),
                auto_apply=bool(raw.get("auto_apply", False)),
                expected_services=tuple(expected),
                health=health,
                services=service_policies,
            )
        )

    excluded_raw = root.get("exclude", [])
    if not isinstance(excluded_raw, list) or not all(
        isinstance(item, str) for item in excluded_raw
    ):
        raise ConfigError("config.exclude must be a list of stack names")
    overlap = {item.name for item in stacks if item.enabled} & set(excluded_raw)
    if overlap:
        raise ConfigError(f"enabled stacks also excluded: {', '.join(sorted(overlap))}")

    try:
        mode = Mode(str(root.get("mode", "monitor")))
    except ValueError as error:
        raise ConfigError("config.mode must be monitor or apply") from error

    max_updates = _positive_int(
        root.get("max_updates_per_run", 1), "config.max_updates_per_run"
    )
    if max_updates != 1:
        raise ConfigError("MVP requires max_updates_per_run: 1")

    fingerprint = (
        str(portainer["tls_fingerprint_sha256"]).lower().replace(":", "")
        if portainer.get("tls_fingerprint_sha256")
        else None
    )
    if fingerprint and (
        len(fingerprint) != 64 or any(character not in string.hexdigits for character in fingerprint)
    ):
        raise ConfigError("portainer.tls_fingerprint_sha256 must be 64 hexadecimal characters")
    if portainer.get("tls_ca_file") and fingerprint:
        raise ConfigError("choose tls_ca_file or tls_fingerprint_sha256, not both")
    if not portainer.get("tls_ca_file") and not fingerprint:
        raise ConfigError(
            "configure exactly one of portainer.tls_ca_file or tls_fingerprint_sha256"
        )

    base_url = str(_required(portainer, "base_url", "portainer")).rstrip("/")
    parsed_base_url = urllib.parse.urlsplit(base_url)
    if parsed_base_url.scheme != "https" or not parsed_base_url.hostname:
        raise ConfigError("portainer.base_url must use https")

    return Policy(
        mode=mode,
        max_updates_per_run=max_updates,
        verification_timeout_seconds=_positive_int(
            root.get("verification_timeout_seconds", 300),
            "config.verification_timeout_seconds",
        ),
        candidate_min_age_seconds=_positive_int(
            root.get("candidate_min_age_seconds", 86400),
            "config.candidate_min_age_seconds",
        ),
        lease_ttl_seconds=_positive_int(
            root.get("lease_ttl_seconds", 1800), "config.lease_ttl_seconds"
        ),
        portainer_base_url=base_url,
        portainer_api_key_file=str(
            _required(portainer, "api_key_file", "portainer")
        ),
        expected_username=str(
            _required(portainer, "expected_username", "portainer")
        ),
        tls_ca_file=(str(portainer["tls_ca_file"]) if portainer.get("tls_ca_file") else None),
        tls_fingerprint_sha256=fingerprint,
        state_file=str(root.get("state_file", "/data/updater.db")),
        check_interval_seconds=_positive_int(
            root.get("check_interval_seconds", 86400),
            "config.check_interval_seconds",
        ),
        stacks=tuple(stacks),
        excluded_stacks=frozenset(excluded_raw),
    )
