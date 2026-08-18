import base64
import json
import urllib.parse

import pytest

from ripen.adapters import (
    AdapterError,
    GitHubProposalAdapter,
    JsonLogNotifier,
    OciRegistryAdapter,
    PortainerHttpAdapter,
    parse_image_reference,
)
from ripen.models import GitProposalChange, GitProposalResult, PortainerStack


class FakePortainerClient:
    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        return 200, [{"Id": "not-an-integer"}]


class RecordingPortainerClient:
    def __init__(self) -> None:
        self.timeout = None

    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        self.timeout = timeout
        return 200, {}


class StackListPortainerClient:
    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        return 200, [
            {
                "Id": 211,
                "EndpointId": 2,
                "Name": "arr",
                "Status": 1,
                "Env": [],
                "GitConfig": {"URL": "https://github.com/example/nas.git"},
            }
        ]


class ServiceImagePortainerClient:
    def __init__(self) -> None:
        self.container_path = ""

    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        if "/containers/json?" in path:
            self.container_path = path
            return 200, [
                {
                    "ImageID": "sha256:radarr-image",
                    "Labels": {
                        "com.docker.compose.project": "arr",
                        "com.docker.compose.service": "radarr",
                    },
                },
                {
                    "ImageID": "sha256:sonarr-image",
                    "Labels": {
                        "com.docker.compose.project": "arr",
                        "com.docker.compose.service": "sonarr",
                    },
                },
            ]
        if path.endswith("/images/sha256%3Aradarr-image/json"):
            return 200, {
                "RepoDigests": [
                    "lscr.io/linuxserver/radarr@sha256:" + "1" * 64
                ]
            }
        if path.endswith("/images/sha256%3Asonarr-image/json"):
            return 200, {
                "RepoDigests": [
                    "lscr.io/linuxserver/sonarr@sha256:" + "2" * 64
                ]
            }
        return 404, {"message": "unexpected request"}


class PinnedServiceImagePortainerClient:
    def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
        if "/containers/json?" in path:
            return 200, [
                {
                    "Image": "lscr.io/linuxserver/bazarr:latest@sha256:" + "2" * 64,
                    "ImageID": "sha256:bazarr-image",
                    "Labels": {
                        "com.docker.compose.project": "arr",
                        "com.docker.compose.service": "bazarr",
                    },
                }
            ]
        if path.endswith("/images/sha256%3Abazarr-image/json"):
            return 200, {
                "RepoDigests": [
                    "lscr.io/linuxserver/bazarr@sha256:" + "1" * 64,
                    "lscr.io/linuxserver/bazarr@sha256:" + "2" * 64,
                ]
            }
        return 404, {"message": "unexpected request"}


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


def test_stack_list_records_git_backing(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    adapter._client = StackListPortainerClient()

    assert adapter.list_stacks()[0].git_backed is True


def test_stack_update_refuses_git_backed_stack(tmp_path) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    client = RecordingPortainerClient()
    adapter._client = client

    with pytest.raises(AdapterError, match="Git-backed stack"):
        adapter.update_stack(
            PortainerStack(211, 2, "arr", 1, (), git_backed=True),
            "services: {}",
            (),
            repull=False,
        )

    assert client.timeout is None


def test_github_proposal_creates_digest_pin_pr(tmp_path) -> None:
    secret = tmp_path / "github-token"
    secret.write_text("github_pat_example\n", encoding="utf-8")
    secret.chmod(0o600)
    adapter = GitHubProposalAdapter("example/nas", "main", str(secret))
    old = "services:\n  radarr:\n    image: example/radarr:latest\n"
    new = old.replace("latest", "latest@sha256:" + "2" * 64)
    calls = []

    def request(method, path, body=None, *, allow_not_found=False):  # noqa: ANN001
        calls.append((method, path, body, allow_not_found))
        if path.startswith("/contents/stacks/arr/compose.yaml?ref=main"):
            return {
                "content": "\n".join(
                    [base64.b64encode(old.encode()).decode()[:20],
                     base64.b64encode(old.encode()).decode()[20:]]
                ),
                "sha": "file-sha",
            }
        if path.startswith("/git/ref/heads/ripen%2F"):
            return None
        if path == "/git/ref/heads/main":
            return {"object": {"sha": "commit-sha"}}
        if method == "GET" and path.startswith("/pulls?"):
            return []
        if method == "POST" and path == "/pulls":
            return {"html_url": "https://github.com/example/nas/pull/42"}
        return {}

    adapter._request = request
    result = adapter.propose(
        GitProposalChange(
            "arr/radarr",
            "stacks/arr/compose.yaml",
            old,
            new,
            "sha256:" + "2" * 64,
        )
    )

    assert result.created is True
    assert result.url.endswith("/pull/42")
    put = next(
        call
        for call in calls
        if call[0:2] == ("PUT", "/contents/stacks/arr/compose.yaml")
    )
    assert base64.b64decode(put[2]["content"]).decode() == new
    assert put[2]["sha"] == "file-sha"


def test_github_proposal_reuses_existing_branch_and_pull_request(tmp_path) -> None:
    secret = tmp_path / "github-token"
    secret.write_text("github_pat_example", encoding="utf-8")
    secret.chmod(0o600)
    adapter = GitHubProposalAdapter("example/nas", "main", str(secret))
    old = "services:\n  radarr:\n    image: example/radarr:latest\n"
    new = old.replace("latest", "latest@sha256:" + "2" * 64)
    calls = []

    def request(method, path, body=None, *, allow_not_found=False):  # noqa: ANN001
        calls.append((method, path, body, allow_not_found))
        if path.endswith("?ref=main"):
            content = old
        elif path.startswith("/git/ref/heads/"):
            return {"object": {"sha": "branch-sha"}}
        elif path.startswith("/contents/"):
            content = new
        elif path.startswith("/pulls?"):
            return [{"html_url": "https://github.com/example/nas/pull/42"}]
        else:
            raise AssertionError((method, path))
        return {
            "content": base64.b64encode(content.encode()).decode(),
            "sha": "file-sha",
        }

    adapter._request = request
    result = adapter.propose(
        GitProposalChange(
            "arr/radarr",
            "stacks/arr/compose.yaml",
            old,
            new,
            "sha256:" + "2" * 64,
        )
    )

    assert result == GitProposalResult(
        "https://github.com/example/nas/pull/42", created=False
    )
    assert not any(call[0] in {"POST", "PUT"} for call in calls)


def test_github_proposal_refuses_repository_source_drift(tmp_path) -> None:
    secret = tmp_path / "github-token"
    secret.write_text("github_pat_example", encoding="utf-8")
    secret.chmod(0o600)
    adapter = GitHubProposalAdapter("example/nas", "main", str(secret))
    actual = "services: {}\n"
    adapter._request = lambda *args, **kwargs: {
        "content": base64.b64encode(actual.encode()).decode(),
        "sha": "file-sha",
    }

    with pytest.raises(AdapterError, match="differs from the live reviewed"):
        adapter.propose(
            GitProposalChange(
                "arr/radarr",
                "stacks/arr/compose.yaml",
                "services:\n  radarr: {}\n",
                "services:\n  radarr:\n    image: example/radarr:latest\n",
                "sha256:" + "2" * 64,
            )
        )


def test_github_token_file_rejects_broad_permissions(tmp_path) -> None:
    secret = tmp_path / "github-token"
    secret.write_text("github_pat_example", encoding="utf-8")
    secret.chmod(0o644)

    with pytest.raises(ValueError, match="group or others"):
        GitHubProposalAdapter("example/nas", "main", str(secret))


def test_running_service_digests_are_scoped_to_the_authorized_compose_project(
    tmp_path,
) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    client = ServiceImagePortainerClient()
    adapter._client = client

    digests = adapter.get_service_image_digests(
        PortainerStack(211, 2, "arr", 1)
    )

    assert digests == {
        "radarr": "sha256:" + "1" * 64,
        "sonarr": "sha256:" + "2" * 64,
    }
    assert "all=1" not in client.container_path
    query = urllib.parse.parse_qs(urllib.parse.urlsplit(client.container_path).query)
    filters = json.loads(query["filters"][0])
    assert filters["label"] == ["com.docker.compose.project=arr"]
    assert filters["status"] == ["running"]


def test_running_service_digests_reject_an_empty_project_result(tmp_path) -> None:
    class EmptyClient:
        def request(self, method, path, headers, body=None, *, timeout=None):  # noqa: ANN001
            return 200, []

    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    adapter._client = EmptyClient()

    with pytest.raises(AdapterError, match="no running containers"):
        adapter.get_service_image_digests(PortainerStack(211, 2, "arr", 1))


def test_running_service_digest_prefers_the_containers_exact_digest_pin(
    tmp_path,
) -> None:
    secret = tmp_path / "api-key"
    secret.write_text("ptr_key", encoding="utf-8")
    adapter = PortainerHttpAdapter(
        "https://portainer:9443", str(secret), fingerprint_sha256="a" * 64
    )
    adapter._client = PinnedServiceImagePortainerClient()

    digests = adapter.get_service_image_digests(PortainerStack(211, 2, "arr", 1))

    assert digests == {"bazarr": "sha256:" + "2" * 64}


def test_registry_rejects_insecure_bearer_realm() -> None:
    challenge = 'Bearer realm="http://auth.example/token",service="registry.example"'

    with pytest.raises(AdapterError, match="realm must use https"):
        OciRegistryAdapter()._bearer_token(challenge)


def test_registry_resolves_the_linux_amd64_manifest_digest(monkeypatch) -> None:
    class Response:
        headers = {
            "Content-Type": "application/vnd.oci.image.index.v1+json",
            "Docker-Content-Digest": "sha256:" + "0" * 64,
        }

        def __enter__(self):
            return self

        def __exit__(self, *args):  # noqa: ANN002
            return None

        def read(self):
            return json.dumps(
                {
                    "manifests": [
                        {
                            "digest": "sha256:" + "1" * 64,
                            "platform": {"os": "linux", "architecture": "arm64"},
                        },
                        {
                            "digest": "sha256:" + "2" * 64,
                            "platform": {"os": "linux", "architecture": "amd64"},
                        },
                    ]
                }
            ).encode()

    monkeypatch.setattr("urllib.request.urlopen", lambda *args, **kwargs: Response())

    digest = OciRegistryAdapter().resolve_platform_digest(
        parse_image_reference("lscr.io/linuxserver/radarr:latest"),
        os_name="linux",
        architecture="amd64",
    )

    assert digest == "sha256:" + "2" * 64


def test_registry_uses_variant_to_disambiguate_arm_manifests(monkeypatch) -> None:
    class Response:
        headers = {"Docker-Content-Digest": "sha256:" + "0" * 64}

        def __enter__(self):
            return self

        def __exit__(self, *args):  # noqa: ANN002
            return None

        def read(self):
            return json.dumps(
                {
                    "manifests": [
                        {"digest": "sha256:" + "6" * 64, "platform": {"os": "linux", "architecture": "arm", "variant": "v6"}},
                        {"digest": "sha256:" + "7" * 64, "platform": {"os": "linux", "architecture": "arm", "variant": "v7"}},
                    ]
                }
            ).encode()

    monkeypatch.setattr("urllib.request.urlopen", lambda *args, **kwargs: Response())

    digest = OciRegistryAdapter().resolve_platform_digest(
        parse_image_reference("example/app:latest"),
        os_name="linux",
        architecture="arm",
        variant="v7",
    )

    assert digest == "sha256:" + "7" * 64


def test_registry_verifies_platform_for_single_manifest(monkeypatch) -> None:
    manifest_digest = "sha256:" + "1" * 64
    config_digest = "sha256:" + "2" * 64

    class Response:
        def __init__(self, payload, digest):  # noqa: ANN001
            self.payload = payload
            self.headers = {"Docker-Content-Digest": digest}

        def __enter__(self):
            return self

        def __exit__(self, *args):  # noqa: ANN002
            return None

        def read(self):
            return json.dumps(self.payload).encode()

    responses = iter(
        [
            Response({"config": {"digest": config_digest}}, manifest_digest),
            Response({"os": "linux", "architecture": "arm64", "variant": "v8"}, config_digest),
        ]
    )
    monkeypatch.setattr("urllib.request.urlopen", lambda *args, **kwargs: next(responses))

    with pytest.raises(AdapterError, match="does not match requested platform"):
        OciRegistryAdapter().resolve_platform_digest(
            parse_image_reference("example/app:latest"),
            os_name="linux",
            architecture="amd64",
        )


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
