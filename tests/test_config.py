from pathlib import Path

import pytest

from ripen.config import ConfigError, load_policy
from ripen.models import Mode


VALID = """
mode: monitor
state_file: /tmp/updater.db
portainer:
  base_url: https://portainer:9443
  api_key_file: /secret
  expected_username: ripen
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


def test_load_policy_supports_git_native_stack_source(tmp_path: Path) -> None:
    value = VALID.replace(
        "portainer:\n",
        "github:\n"
        "  repository: example/nas\n"
        "  base_branch: main\n"
        "  token_file: /run/secrets/github_token\n"
        "portainer:\n",
    ).replace(
        "    expected_services: [example-app]\n",
        "    expected_services: [example-app]\n"
        "    git_path: stacks/example-app/compose.yaml\n",
    )

    policy = load_policy(write(tmp_path, value))

    assert policy.github is not None
    assert policy.github.repository == "example/nas"
    assert policy.stacks[0].git_path == "stacks/example-app/compose.yaml"


def test_git_path_requires_github_configuration(tmp_path: Path) -> None:
    value = VALID.replace(
        "    expected_services: [example-app]\n",
        "    expected_services: [example-app]\n"
        "    git_path: stacks/example-app/compose.yaml\n",
    )

    with pytest.raises(ConfigError, match="requires github configuration"):
        load_policy(write(tmp_path, value))


def test_load_policy_supports_explicit_per_service_health_rules(tmp_path: Path) -> None:
    value = VALID.replace(
        "  example-app:\n"
        "    enabled: true\n"
        "    auto_apply: false\n"
        "    expected_services: [example-app]\n"
        "    health:\n"
        "      type: http\n"
        "      url: http://nas:8091/\n",
        "  arr:\n"
        "    enabled: true\n"
        "    expected_services: [radarr, sonarr]\n"
        "    services:\n"
        "      radarr:\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://radarr:7878/\n"
        "          accepted_status: [200, 302]\n"
        "      sonarr:\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://sonarr:8989/\n"
        "          accepted_status: [200, 302]\n",
    ).replace("exclude: [arr]", "exclude: []")

    policy = load_policy(write(tmp_path, value))

    stack = policy.stacks[0]
    assert stack.name == "arr"
    assert [service.name for service in stack.services] == ["radarr", "sonarr"]
    assert stack.services[0].health.target == "http://radarr:7878/"
    assert stack.services[0].health.accepted_status == (200, 302)


def test_multi_service_policy_supports_health_only_service(tmp_path: Path) -> None:
    value = VALID.replace(
        "  example-app:\n"
        "    enabled: true\n"
        "    auto_apply: false\n"
        "    expected_services: [example-app]\n"
        "    health:\n"
        "      type: http\n"
        "      url: http://nas:8091/\n",
        "  arr:\n"
        "    enabled: true\n"
        "    expected_services: [radarr, readarr]\n"
        "    services:\n"
        "      radarr:\n"
        "        enabled: true\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://radarr:7878/\n"
        "      readarr:\n"
        "        enabled: false\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://readarr:8787/\n",
    ).replace("exclude: [arr]", "exclude: []")

    policy = load_policy(write(tmp_path, value))

    assert policy.stacks[0].services[0].enabled is True
    assert policy.stacks[0].services[1].enabled is False


def test_multi_service_policy_requires_one_managed_service(tmp_path: Path) -> None:
    value = VALID.replace(
        "    auto_apply: false\n"
        "    expected_services: [example-app]\n"
        "    health:\n"
        "      type: http\n"
        "      url: http://nas:8091/\n",
        "    expected_services: [example-app]\n"
        "    services:\n"
        "      example-app:\n"
        "        enabled: false\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://example-app:8091/\n",
    )

    with pytest.raises(ConfigError, match="at least one enabled service"):
        load_policy(write(tmp_path, value))


@pytest.mark.parametrize(
    ("field", "value"),
    (("enabled", '"false"'), ("auto_apply", '"true"')),
)
def test_per_service_flags_require_real_booleans(
    tmp_path: Path, field: str, value: str
) -> None:
    service = (
        "    auto_apply: false\n"
        "    expected_services: [example-app]\n"
        "    health:\n"
        "      type: http\n"
        "      url: http://nas:8091/\n"
    )
    replacement = (
        "    expected_services: [example-app]\n"
        "    services:\n"
        "      example-app:\n"
        f"        {field}: {value}\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://example-app:8091/\n"
    )

    with pytest.raises(ConfigError, match=f"{field} must be a boolean"):
        load_policy(write(tmp_path, VALID.replace(service, replacement)))


def test_multi_service_policy_requires_explicit_service_rules(tmp_path: Path) -> None:
    value = VALID.replace(
        "expected_services: [example-app]",
        "expected_services: [example-app, sidecar]",
    )

    with pytest.raises(ConfigError, match="requires per-service rules"):
        load_policy(write(tmp_path, value))


def test_expected_services_rejects_duplicate_names(tmp_path: Path) -> None:
    value = VALID.replace(
        "expected_services: [example-app]",
        "expected_services: [example-app, example-app]",
    )

    with pytest.raises(ConfigError, match="duplicate"):
        load_policy(write(tmp_path, value))


@pytest.mark.parametrize("statuses", ["[]", "[true]", "[99]", "[600]"])
def test_health_statuses_are_nonempty_http_codes(
    tmp_path: Path, statuses: str
) -> None:
    value = VALID.replace(
        "      url: http://nas:8091/",
        f"      url: http://nas:8091/\n      accepted_status: {statuses}",
    )

    with pytest.raises(ConfigError, match="non-empty list of HTTP status codes"):
        load_policy(write(tmp_path, value))


def test_per_service_policy_rejects_ambiguous_stack_level_apply_setting(
    tmp_path: Path,
) -> None:
    value = VALID.replace(
        "    auto_apply: false\n"
        "    expected_services: [example-app]\n"
        "    health:\n"
        "      type: http\n"
        "      url: http://nas:8091/\n",
        "    auto_apply: true\n"
        "    expected_services: [example-app]\n"
        "    services:\n"
        "      example-app:\n"
        "        auto_apply: false\n"
        "        health:\n"
        "          type: http\n"
        "          url: http://example-app:8091/\n",
    )

    with pytest.raises(ConfigError, match="cannot use stack-level auto_apply or health"):
        load_policy(write(tmp_path, value))


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
