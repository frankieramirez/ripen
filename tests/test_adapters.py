import json

import pytest

from nas_stack_updater.adapters import (
    AdapterError,
    JsonLogNotifier,
    OciRegistryAdapter,
    PortainerHttpAdapter,
)
from nas_stack_updater.models import PortainerStack


class FakePortainerClient:
    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        return 200, [{"Id": "not-an-integer"}]


class RecordingPortainerClient:
    def __init__(self) -> None:
        self.timeout = None

    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        self.timeout = timeout
        return 200, {}


def test_malformed_portainer_stack_is_reported_as_adapter_error(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    adapter._client = FakePortainerClient()

    with pytest.raises(AdapterError, match="unexpected Portainer stack entry"):
        adapter.list_stacks()


def test_api_key_file_tolerates_trailing_newline(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key\n", encoding="utf-8")

    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )

    assert adapter._headers["X-API-Key"] == "ptr_key"


def test_api_key_file_rejects_interior_whitespace(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr key", encoding="utf-8")

    with pytest.raises(ValueError, match="whitespace"):
        PortainerHttpAdapter(
            "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
        )


def test_stack_update_uses_the_extended_deployment_timeout(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443",
        str(secret),
        fingerprint_sha256="a" * 64,
        update_timeout=600,
    )
    client = RecordingPortainerClient()
    adapter._client = client

    adapter.update_stack(
        PortainerStack(147, 2, "example-app", 1),
        "services: {}",
        (),
        repull=True,
    )

    assert client.timeout == 600


def test_registry_rejects_insecure_bearer_realm() -> None:
    challenge = 'Bearer realm="http://auth.example/token",service="registry.example"'

    with pytest.raises(AdapterError, match="realm must use https"):
        OciRegistryAdapter()._bearer_token(challenge)


def test_json_notifier_redacts_secret_like_fields(capsys) -> None:
    JsonLogNotifier().emit(
        "test",
        {
            "stack": "example-app",
            "authorization": "Bearer exposed",
            "apiKey": "exposed",
            "nested": {"token": "also exposed"},
        },
    )

    payload = json.loads(capsys.readouterr().err)
    assert payload["stack"] == "example-app"
    assert payload["authorization"] == "[redacted]"
    assert payload["apiKey"] == "[redacted]"
    assert payload["nested"]["token"] == "[redacted]"
