from pathlib import Path

import pytest

from nas_stack_updater.config import ConfigError, load_policy
from nas_stack_updater.models import Mode


VALID = """
mode: monitor
state_file: /tmp/updater.db
portainer:
  base_url: https://portainer:9443
  api_key_file: /secret
  expected_username: nas-stack-updater
  tls_fingerprint_sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
stacks:
  example-app:
    enabled: true
    auto_apply: false
    expected_services: [example-app]
    health:
      type: http
      url: http://nas:8091/
exclude: [arr]
"""


def write(tmp_path: Path, value: str) -> Path:
    path = tmp_path / "policy.yaml"
    path.write_text(value, encoding="utf-8")
    return path


def test_load_policy_defaults_to_single_update_monitor_mode(tmp_path: Path) -> None:
    policy = load_policy(write(tmp_path, VALID))

    assert policy.mode is Mode.MONITOR
    assert policy.max_updates_per_run == 1
    assert policy.stacks[0].name == "example-app"
    assert policy.stacks[0].auto_apply is False


def test_unknown_field_is_rejected(tmp_path: Path) -> None:
    with pytest.raises(ConfigError, match="unknown config fields"):
        load_policy(write(tmp_path, VALID + "surprise: true\n"))


def test_enabled_stack_cannot_also_be_excluded(tmp_path: Path) -> None:
    value = VALID.replace("exclude: [arr]", "exclude: [example-app]")

    with pytest.raises(ConfigError, match="also excluded"):
        load_policy(write(tmp_path, value))


def test_more_than_one_update_per_run_is_rejected(tmp_path: Path) -> None:
    value = VALID.replace("mode: monitor", "mode: monitor\nmax_updates_per_run: 2")

    with pytest.raises(ConfigError, match="requires max_updates_per_run"):
        load_policy(write(tmp_path, value))


def test_invalid_tls_fingerprint_is_rejected(tmp_path: Path) -> None:
    value = VALID.replace("a" * 64, "abcdef")

    with pytest.raises(ConfigError, match="64 hexadecimal"):
        load_policy(write(tmp_path, value))


def test_portainer_base_url_must_use_https(tmp_path: Path) -> None:
    value = VALID.replace("https://portainer:9443", "http://portainer:9000")

    with pytest.raises(ConfigError, match="base_url must use https"):
        load_policy(write(tmp_path, value))


def test_tls_trust_mechanism_is_required(tmp_path: Path) -> None:
    value = VALID.replace("  tls_fingerprint_sha256: " + "a" * 64 + "\n", "")

    with pytest.raises(ConfigError, match="exactly one"):
        load_policy(write(tmp_path, value))


def test_malformed_numeric_setting_is_a_config_error(tmp_path: Path) -> None:
    value = VALID.replace("mode: monitor", "mode: monitor\nlease_ttl_seconds: nope")

    with pytest.raises(ConfigError, match="lease_ttl_seconds must be an integer"):
        load_policy(write(tmp_path, value))
