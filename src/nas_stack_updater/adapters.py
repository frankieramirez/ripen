from __future__ import annotations

import base64
import hashlib
import hmac
import http.client
import json
import re
import secrets
import socket
import sqlite3
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Any

from .models import (
    CandidateObservation,
    GitProposalChange,
    GitProposalResult,
    HealthPolicy,
    ImageReference,
    PendingProposal,
    PortainerStack,
    UpdaterStatus,
)


class AdapterError(RuntimeError):
    pass


class PortainerError(AdapterError):
    def __init__(self, status: int, detail: str) -> None:
        super().__init__(f"Portainer HTTP {status}: {detail}")
        self.status = status


class GitHubError(AdapterError):
    def __init__(self, status: int, detail: str) -> None:
        super().__init__(f"GitHub HTTP {status}: {detail}")
        self.status = status


_REPOSITORY_PATTERN = re.compile(
    r"^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*"
    r"(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$"
)
_TAG_PATTERN = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$")


class JsonHttpsClient:
    def __init__(
        self,
        base_url: str,
        *,
        ca_file: str | None = None,
        fingerprint_sha256: str | None = None,
        timeout: float = 20,
    ) -> None:
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme != "https" or not parsed.hostname:
            raise ValueError("Portainer base_url must use https")
        self.host = parsed.hostname
        self.port = parsed.port or 443
        self.base_path = parsed.path.rstrip("/")
        self.ca_file = ca_file
        self.fingerprint = fingerprint_sha256
        self.timeout = timeout

    def request(
        self,
        method: str,
        path: str,
        headers: dict[str, str],
        body: dict[str, object] | None = None,
        *,
        timeout: float | None = None,
    ) -> tuple[int, object]:
        if self.fingerprint:
            context = ssl._create_unverified_context()  # noqa: SLF001 - verified below
            check_hostname = False
        else:
            context = ssl.create_default_context(cafile=self.ca_file)
            check_hostname = True
        context.check_hostname = check_hostname
        connection = http.client.HTTPSConnection(
            self.host,
            self.port,
            timeout=self.timeout if timeout is None else timeout,
            context=context,
        )
        encoded = None
        request_headers = {"Accept": "application/json", **headers}
        if body is not None:
            encoded = json.dumps(body, separators=(",", ":")).encode()
            request_headers["Content-Type"] = "application/json"
        try:
            connection.connect()
            if self.fingerprint:
                if connection.sock is None:
                    raise AdapterError("TLS socket unavailable")
                peer = connection.sock.getpeercert(binary_form=True)
                actual = hashlib.sha256(peer).hexdigest().lower().replace(":", "")
                expected = self.fingerprint.lower().replace(":", "")
                if not hmac.compare_digest(actual, expected):
                    raise AdapterError("Portainer TLS fingerprint mismatch")
            target = f"{self.base_path}{path}"
            connection.request(method, target, body=encoded, headers=request_headers)
            response = connection.getresponse()
            raw = response.read()
        finally:
            connection.close()
        if not raw:
            payload: object = None
        else:
            try:
                payload = json.loads(raw)
            except json.JSONDecodeError:
                payload = {"message": "non-JSON response"}
        return response.status, payload


class PortainerHttpAdapter:
    def __init__(
        self,
        base_url: str,
        api_key_file: str,
        *,
        ca_file: str | None = None,
        fingerprint_sha256: str | None = None,
        timeout: float = 20,
        update_timeout: float = 600,
    ) -> None:
        key = Path(api_key_file).read_text(encoding="utf-8").strip()
        if not key or any(character.isspace() for character in key):
            raise ValueError("Portainer API key file is empty or contains whitespace")
        self._headers = {"X-API-Key": key}
        self._client = JsonHttpsClient(
            base_url,
            ca_file=ca_file,
            fingerprint_sha256=fingerprint_sha256,
            timeout=timeout,
        )
        self._update_timeout = update_timeout

    def _request(
        self,
        method: str,
        path: str,
        body: dict[str, object] | None = None,
        *,
        timeout: float | None = None,
    ) -> object:
        status, payload = self._client.request(
            method, path, self._headers, body, timeout=timeout
        )
        if status < 200 or status >= 300:
            detail = "request failed"
            if isinstance(payload, dict):
                detail = str(payload.get("message", payload.get("details", detail)))
            raise PortainerError(status, detail)
        return payload

    def current_username(self) -> str:
        payload = self._request("GET", "/api/users/me")
        if not isinstance(payload, dict) or not isinstance(payload.get("Username"), str):
            raise AdapterError("unexpected Portainer user response")
        return payload["Username"]

    def list_stacks(self) -> tuple[PortainerStack, ...]:
        payload = self._request("GET", "/api/stacks")
        if not isinstance(payload, list):
            raise AdapterError("unexpected Portainer stacks response")
        stacks: list[PortainerStack] = []
        for item in payload:
            if not isinstance(item, dict):
                continue
            raw_env = item.get("Env") or []
            if not isinstance(raw_env, list):
                raise AdapterError("unexpected Portainer stack entry")
            env = tuple(
                {"name": str(pair.get("name", "")), "value": str(pair.get("value", ""))}
                for pair in raw_env
                if isinstance(pair, dict)
            )
            try:
                stacks.append(
                    PortainerStack(
                        id=int(item["Id"]),
                        endpoint_id=int(item["EndpointId"]),
                        name=str(item["Name"]),
                        status=int(item["Status"]),
                        env=env,
                        git_backed=bool(item.get("GitConfig")),
                    )
                )
            except (KeyError, TypeError, ValueError) as error:
                raise AdapterError("unexpected Portainer stack entry") from error
        return tuple(stacks)

    def get_stack_file(self, stack_id: int) -> str:
        payload = self._request("GET", f"/api/stacks/{stack_id}/file")
        if not isinstance(payload, dict) or not isinstance(
            payload.get("StackFileContent"), str
        ):
            raise AdapterError("unexpected Portainer stack file response")
        return payload["StackFileContent"]

    def get_image_status(self, stack_id: int) -> str:
        payload = self._request("GET", f"/api/stacks/{stack_id}/images_status")
        if not isinstance(payload, dict) or not isinstance(payload.get("Status"), str):
            raise AdapterError("unexpected Portainer image status response")
        return payload["Status"].lower()

    def get_service_image_digests(self, stack: PortainerStack) -> dict[str, str]:
        filters = json.dumps(
            {
                "label": [f"com.docker.compose.project={stack.name}"],
                "status": ["running"],
            },
            separators=(",", ":"),
        )
        path = (
            f"/api/endpoints/{stack.endpoint_id}/docker/containers/json?"
            + urllib.parse.urlencode({"filters": filters})
        )
        payload = self._request("GET", path)
        if not isinstance(payload, list):
            raise AdapterError("unexpected Portainer container response")
        result: dict[str, str] = {}
        for item in payload:
            if not isinstance(item, dict):
                raise AdapterError("unexpected Portainer container entry")
            labels = item.get("Labels")
            image_id = item.get("ImageID")
            container_image = item.get("Image")
            if not isinstance(labels, dict) or not isinstance(image_id, str):
                raise AdapterError("Portainer container entry is missing image metadata")
            service = labels.get("com.docker.compose.service")
            if labels.get("com.docker.compose.project") != stack.name:
                raise AdapterError("Portainer returned a container from another project")
            if not isinstance(service, str) or not service or service in result:
                raise AdapterError("Portainer returned an invalid Compose service identity")
            if isinstance(container_image, str):
                pinned = re.search(r"@(sha256:[0-9a-f]{64})$", container_image)
                if pinned:
                    result[service] = pinned.group(1)
                    continue
            image_payload = self._request(
                "GET",
                f"/api/endpoints/{stack.endpoint_id}/docker/images/"
                f"{urllib.parse.quote(image_id, safe='')}/json",
            )
            if not isinstance(image_payload, dict) or not isinstance(
                image_payload.get("RepoDigests"), list
            ):
                raise AdapterError("unexpected Portainer image response")
            digests = [
                value.rsplit("@", 1)[1]
                for value in image_payload["RepoDigests"]
                if isinstance(value, str)
                and "@" in value
                and value.rsplit("@", 1)[1].startswith("sha256:")
            ]
            if len(set(digests)) != 1:
                raise AdapterError(
                    f"Portainer image for service {service!r} has no unique digest"
                )
            result[service] = digests[0]
        if not result:
            raise AdapterError(
                f"Portainer returned no running containers for project {stack.name!r}"
            )
        return result

    def update_stack(
        self,
        stack: PortainerStack,
        compose: str,
        env: tuple[dict[str, str], ...],
        *,
        repull: bool,
    ) -> None:
        if stack.git_backed:
            raise AdapterError(
                "refusing direct update of a Git-backed stack; deploy through Git"
            )
        body: dict[str, object] = {
            "Env": list(env),
            "Prune": False,
            "RepullImageAndRedeploy": repull,
            "StackFileContent": compose,
        }
        self._request(
            "PUT",
            f"/api/stacks/{stack.id}?endpointId={stack.endpoint_id}",
            body,
            timeout=self._update_timeout,
        )


class GitHubProposalAdapter:
    """Creates one idempotent digest-pin PR without merging or deploying it."""

    def __init__(
        self,
        repository: str,
        base_branch: str,
        token_file: str,
        *,
        timeout: float = 20,
    ) -> None:
        owner, separator, name = repository.partition("/")
        if not separator or not owner or not name or "/" in name:
            raise ValueError("GitHub repository must be owner/repository")
        token = Path(token_file).read_text(encoding="utf-8").strip()
        if not token or any(character.isspace() for character in token):
            raise ValueError("GitHub token file is empty or contains whitespace")
        if Path(token_file).stat().st_mode & 0o077:
            raise ValueError("GitHub token file must not be accessible by group or others")
        self.repository = repository
        self.owner = owner
        self.base_branch = base_branch
        self.timeout = timeout
        self._headers = {
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
        }

    def _request(
        self,
        method: str,
        path: str,
        body: dict[str, object] | None = None,
        *,
        allow_not_found: bool = False,
    ) -> object | None:
        url = f"https://api.github.com/repos/{self.repository}{path}"
        encoded = json.dumps(body, separators=(",", ":")).encode() if body else None
        headers = dict(self._headers)
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(url, data=encoded, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
        except urllib.error.HTTPError as error:
            if allow_not_found and error.code == 404:
                return None
            try:
                payload = json.loads(error.read())
            except (json.JSONDecodeError, OSError):
                payload = {}
            detail = str(payload.get("message", "request failed"))
            raise GitHubError(error.code, detail) from error
        except (urllib.error.URLError, TimeoutError) as error:
            raise AdapterError("GitHub request failed") from error
        if not raw:
            return None
        try:
            return json.loads(raw)
        except json.JSONDecodeError as error:
            raise AdapterError("GitHub returned malformed JSON") from error

    @staticmethod
    def _file(payload: object) -> tuple[str, str]:
        if not isinstance(payload, dict):
            raise AdapterError("GitHub returned an invalid repository file")
        encoded = payload.get("content")
        sha = payload.get("sha")
        if not isinstance(encoded, str) or not isinstance(sha, str):
            raise AdapterError("GitHub repository file is missing content or sha")
        try:
            content = base64.b64decode(
                "".join(encoded.split()), validate=True
            ).decode("utf-8")
        except (ValueError, UnicodeDecodeError) as error:
            raise AdapterError("GitHub repository file content is invalid") from error
        return content, sha

    @staticmethod
    def _slug(value: str) -> str:
        slug = re.sub(r"[^a-z0-9-]+", "-", value.lower()).strip("-")
        if not slug:
            raise ValueError("proposal state key has no safe branch name")
        return slug[:80]

    def propose(self, change: GitProposalChange) -> GitProposalResult:
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", change.digest):
            raise ValueError("Git proposal digest must be a sha256 digest")
        source_path = Path(change.repository_path)
        if (
            source_path.is_absolute()
            or ".." in source_path.parts
            or "\\" in change.repository_path
            or source_path.suffix not in {".yaml", ".yml"}
        ):
            raise ValueError("Git proposal repository path must be a relative YAML path")
        path = urllib.parse.quote(change.repository_path, safe="/")
        base_ref = urllib.parse.quote(self.base_branch, safe="")
        base_payload = self._request("GET", f"/contents/{path}?ref={base_ref}")
        base_content, base_file_sha = self._file(base_payload)
        if not hmac.compare_digest(base_content, change.expected_content):
            raise AdapterError(
                "Git repository source differs from the live reviewed Compose file"
            )

        digest_short = change.digest.removeprefix("sha256:")[:12]
        branch = f"nas-stack-updater/{self._slug(change.state_key)}-{digest_short}"
        encoded_branch = urllib.parse.quote(branch, safe="")
        branch_payload = self._request(
            "GET", f"/git/ref/heads/{encoded_branch}", allow_not_found=True
        )
        if branch_payload is None:
            source_ref = self._request("GET", f"/git/ref/heads/{base_ref}")
            try:
                source_sha = source_ref["object"]["sha"]  # type: ignore[index]
            except (KeyError, TypeError) as error:
                raise AdapterError("GitHub base branch response is invalid") from error
            self._request(
                "POST",
                "/git/refs",
                {"ref": f"refs/heads/{branch}", "sha": source_sha},
            )
            branch_content = base_content
            branch_file_sha = base_file_sha
        else:
            branch_file = self._request("GET", f"/contents/{path}?ref={encoded_branch}")
            branch_content, branch_file_sha = self._file(branch_file)
            if branch_content not in {change.expected_content, change.proposed_content}:
                raise AdapterError("existing updater branch contains an unexpected change")

        if branch_content != change.proposed_content:
            self._request(
                "PUT",
                f"/contents/{path}",
                {
                    "message": f"Pin {change.state_key} to {digest_short}",
                    "content": base64.b64encode(
                        change.proposed_content.encode("utf-8")
                    ).decode("ascii"),
                    "sha": branch_file_sha,
                    "branch": branch,
                },
            )

        query = urllib.parse.urlencode(
            {"state": "open", "head": f"{self.owner}:{branch}", "base": self.base_branch}
        )
        pulls = self._request("GET", f"/pulls?{query}")
        if isinstance(pulls, list) and len(pulls) == 1:
            url = pulls[0].get("html_url") if isinstance(pulls[0], dict) else None
            if isinstance(url, str):
                return GitProposalResult(url=url, created=False)
        if pulls != []:
            raise AdapterError("GitHub returned an ambiguous pull request result")

        pull = self._request(
            "POST",
            "/pulls",
            {
                "title": f"Pin {change.state_key} to {digest_short}",
                "head": branch,
                "base": self.base_branch,
                "body": (
                    "Automated digest-pin proposal from nas-stack-updater.\n\n"
                    f"Service: `{change.state_key}`\n"
                    f"Digest: `{change.digest}`\n\n"
                    "This proposal does not merge or deploy itself."
                ),
            },
        )
        url = pull.get("html_url") if isinstance(pull, dict) else None
        if not isinstance(url, str):
            raise AdapterError("GitHub pull request response is invalid")
        return GitProposalResult(url=url, created=True)


def parse_image_reference(value: str) -> ImageReference:
    original = value.strip()
    if not original:
        raise ValueError("image must be a mutable tagged reference")
    tagged_part, separator, pinned_digest = original.partition("@")
    if separator and (
        "@" in pinned_digest
        or not re.fullmatch(r"sha256:[0-9a-f]{64}", pinned_digest)
    ):
        raise ValueError("image digest pin must be a sha256 digest")
    last_segment = tagged_part.rsplit("/", 1)[-1]
    if ":" in last_segment:
        repository_part, tag = tagged_part.rsplit(":", 1)
    else:
        repository_part, tag = tagged_part, "latest"
    first = repository_part.split("/", 1)[0]
    if "." in first or ":" in first or first == "localhost":
        registry, repository = repository_part.split("/", 1)
    else:
        registry = "registry-1.docker.io"
        repository = repository_part
        if "/" not in repository:
            repository = f"library/{repository}"
    if not _REPOSITORY_PATTERN.fullmatch(repository):
        raise ValueError("image repository is not a valid OCI reference")
    if not _TAG_PATTERN.fullmatch(tag):
        raise ValueError("image tag is not a valid OCI tag")
    return ImageReference(
        registry=registry,
        repository=repository,
        tag=tag,
        original=original,
        pinned_digest=(pinned_digest if separator else None),
    )


class OciRegistryAdapter:
    _ACCEPT = ", ".join(
        (
            "application/vnd.oci.image.index.v1+json",
            "application/vnd.oci.image.manifest.v1+json",
            "application/vnd.docker.distribution.manifest.list.v2+json",
            "application/vnd.docker.distribution.manifest.v2+json",
        )
    )

    def __init__(self, timeout: float = 20) -> None:
        self.timeout = timeout

    def resolve_digest(self, image: ImageReference) -> str:
        url = f"https://{image.registry}/v2/{image.repository}/manifests/{image.tag}"
        request = urllib.request.Request(url, method="HEAD", headers={"Accept": self._ACCEPT})
        try:
            response = urllib.request.urlopen(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise AdapterError(f"registry HTTP {error.code}") from error
            challenge = error.headers.get("WWW-Authenticate", "")
            token = self._bearer_token(challenge)
            request.add_header("Authorization", f"Bearer {token}")
            try:
                response = urllib.request.urlopen(request, timeout=self.timeout)
            except urllib.error.HTTPError as second_error:
                raise AdapterError(f"registry HTTP {second_error.code}") from second_error
            except (urllib.error.URLError, TimeoutError) as second_error:
                raise AdapterError("registry request failed") from second_error
        except (urllib.error.URLError, TimeoutError) as error:
            raise AdapterError("registry request failed") from error
        with response:
            digest = response.headers.get("Docker-Content-Digest")
        if not digest or not digest.startswith("sha256:"):
            raise AdapterError("registry did not return a sha256 digest")
        return digest

    def resolve_platform_digest(
        self,
        image: ImageReference,
        *,
        os_name: str,
        architecture: str,
        variant: str | None = None,
    ) -> str:
        url = f"https://{image.registry}/v2/{image.repository}/manifests/{image.tag}"
        request = urllib.request.Request(
            url, method="GET", headers={"Accept": self._ACCEPT}
        )
        authorization: str | None = None
        try:
            response = urllib.request.urlopen(request, timeout=self.timeout)
        except urllib.error.HTTPError as error:
            if error.code != 401:
                raise AdapterError(f"registry HTTP {error.code}") from error
            challenge = error.headers.get("WWW-Authenticate", "")
            authorization = f"Bearer {self._bearer_token(challenge)}"
            request.add_header("Authorization", authorization)
            try:
                response = urllib.request.urlopen(request, timeout=self.timeout)
            except urllib.error.HTTPError as second_error:
                raise AdapterError(f"registry HTTP {second_error.code}") from second_error
            except (urllib.error.URLError, TimeoutError) as second_error:
                raise AdapterError("registry request failed") from second_error
        except (urllib.error.URLError, TimeoutError) as error:
            raise AdapterError("registry request failed") from error
        with response:
            header_digest = response.headers.get("Docker-Content-Digest")
            try:
                payload = json.loads(response.read())
            except json.JSONDecodeError as error:
                raise AdapterError("registry returned malformed manifest JSON") from error
        manifests = payload.get("manifests") if isinstance(payload, dict) else None
        if isinstance(manifests, list):
            matches = [
                item.get("digest")
                for item in manifests
                if isinstance(item, dict)
                and isinstance(item.get("platform"), dict)
                and item["platform"].get("os") == os_name
                and item["platform"].get("architecture") == architecture
                and (variant is None or item["platform"].get("variant") == variant)
                and isinstance(item.get("digest"), str)
                and item["digest"].startswith("sha256:")
            ]
            if len(matches) != 1:
                raise AdapterError(
                    f"registry returned {len(matches)} manifests for "
                    f"{os_name}/{architecture}"
                    + (f"/{variant}" if variant else "")
                )
            return matches[0]
        if not isinstance(header_digest, str) or not header_digest.startswith("sha256:"):
            raise AdapterError("registry did not return a sha256 digest")
        config = payload.get("config") if isinstance(payload, dict) else None
        config_digest = config.get("digest") if isinstance(config, dict) else None
        if not isinstance(config_digest, str) or not re.fullmatch(
            r"sha256:[0-9a-f]{64}", config_digest
        ):
            raise AdapterError("registry manifest has no verifiable config digest")
        config_request = urllib.request.Request(
            f"https://{image.registry}/v2/{image.repository}/blobs/{config_digest}",
            method="GET",
        )
        if authorization:
            config_request.add_header("Authorization", authorization)
        try:
            with urllib.request.urlopen(config_request, timeout=self.timeout) as response:
                config_payload = json.load(response)
        except urllib.error.HTTPError as error:
            raise AdapterError(f"registry config HTTP {error.code}") from error
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            raise AdapterError("registry config request failed") from error
        if (
            not isinstance(config_payload, dict)
            or config_payload.get("os") != os_name
            or config_payload.get("architecture") != architecture
            or (variant is not None and config_payload.get("variant") != variant)
        ):
            raise AdapterError("registry manifest does not match requested platform")
        return header_digest

    def _bearer_token(self, challenge: str) -> str:
        if not challenge.lower().startswith("bearer "):
            raise AdapterError("unsupported registry authentication challenge")
        values: dict[str, str] = {}
        for item in challenge[7:].split(","):
            key, separator, value = item.strip().partition("=")
            if separator:
                values[key] = value.strip().strip('"')
        realm = values.pop("realm", None)
        if not realm:
            raise AdapterError("registry bearer challenge missing realm")
        parsed_realm = urllib.parse.urlsplit(realm)
        if parsed_realm.scheme != "https" or not parsed_realm.hostname:
            raise AdapterError("registry bearer realm must use https")
        query = dict(urllib.parse.parse_qsl(parsed_realm.query, keep_blank_values=True))
        query.update(values)
        url = urllib.parse.urlunsplit(
            parsed_realm._replace(query=urllib.parse.urlencode(query))
        )
        try:
            with urllib.request.urlopen(url, timeout=self.timeout) as response:
                payload = json.load(response)
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as error:
            raise AdapterError("registry token request failed") from error
        token = payload.get("token") or payload.get("access_token")
        if not isinstance(token, str) or not token:
            raise AdapterError("registry token response missing token")
        return token


class FunctionalHealthAdapter:
    def check(self, policy: HealthPolicy) -> bool:
        if policy.type == "http":
            request = urllib.request.Request(policy.target, method="GET")
            try:
                with urllib.request.urlopen(
                    request, timeout=policy.timeout_seconds
                ) as response:
                    return response.status in policy.accepted_status
            except urllib.error.HTTPError as error:
                return error.code in policy.accepted_status
            except (urllib.error.URLError, TimeoutError):
                return False
        if policy.type == "tcp":
            host, separator, port = policy.target.rpartition(":")
            if not separator:
                raise ValueError("TCP health target must be host:port")
            try:
                with socket.create_connection(
                    (host, int(port)), timeout=policy.timeout_seconds
                ):
                    return True
            except OSError:
                return False
        raise ValueError(f"unsupported health type: {policy.type}")


class SqliteStateStore:
    def __init__(self, path: str | Path) -> None:
        self.path = str(path)
        Path(self.path).parent.mkdir(parents=True, exist_ok=True)
        self._initialize()

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.path, timeout=10)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA journal_mode=WAL")
        connection.execute("PRAGMA foreign_keys=ON")
        return connection

    def _initialize(self) -> None:
        with self._connect() as connection:
            connection.executescript(
                """
                CREATE TABLE IF NOT EXISTS accepted_digests (
                    stack TEXT PRIMARY KEY,
                    digest TEXT NOT NULL,
                    accepted_at TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS candidates (
                    stack TEXT NOT NULL,
                    digest TEXT NOT NULL,
                    first_seen TEXT NOT NULL,
                    last_seen TEXT NOT NULL,
                    observation_count INTEGER NOT NULL,
                    PRIMARY KEY (stack, digest)
                );
                CREATE TABLE IF NOT EXISTS pending_proposals (
                    stack TEXT PRIMARY KEY,
                    digest TEXT NOT NULL,
                    url TEXT NOT NULL,
                    proposed_at TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS attempts (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    stack TEXT NOT NULL,
                    old_digest TEXT NOT NULL,
                    new_digest TEXT NOT NULL,
                    result TEXT NOT NULL,
                    attempted_at TEXT NOT NULL,
                    detail TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS breaker (
                    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                    is_open INTEGER NOT NULL,
                    reason TEXT,
                    changed_at TEXT NOT NULL,
                    clear_reason TEXT
                );
                CREATE TABLE IF NOT EXISTS lease (
                    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
                    acquired_at TEXT NOT NULL,
                    expires_at TEXT NOT NULL,
                    owner_token TEXT NOT NULL
                );
                """
            )
            lease_columns = {
                row[1] for row in connection.execute("PRAGMA table_info(lease)")
            }
            if "owner_token" not in lease_columns:
                connection.execute(
                    "ALTER TABLE lease ADD COLUMN owner_token TEXT NOT NULL DEFAULT ''"
                )

    def acquire_lease(self, now: datetime, ttl_seconds: int) -> str | None:
        expires = now + timedelta(seconds=ttl_seconds)
        owner_token = secrets.token_urlsafe(32)
        with self._connect() as connection:
            connection.execute("BEGIN IMMEDIATE")
            row = connection.execute(
                "SELECT expires_at FROM lease WHERE singleton = 1"
            ).fetchone()
            if row and datetime.fromisoformat(row["expires_at"]) > now:
                return None
            connection.execute("DELETE FROM lease WHERE singleton = 1")
            connection.execute(
                "INSERT INTO lease(singleton, acquired_at, expires_at, owner_token) VALUES(1, ?, ?, ?)",
                (now.isoformat(), expires.isoformat(), owner_token),
            )
            return owner_token

    def release_lease(self, owner_token: str) -> None:
        with self._connect() as connection:
            connection.execute(
                "DELETE FROM lease WHERE singleton = 1 AND owner_token = ?",
                (owner_token,),
            )

    def get_status(self, now: datetime) -> UpdaterStatus:
        with self._connect() as connection:
            breaker = connection.execute(
                "SELECT is_open, reason FROM breaker WHERE singleton = 1"
            ).fetchone()
            digests = {
                row["stack"]: row["digest"]
                for row in connection.execute(
                    "SELECT stack, digest FROM accepted_digests ORDER BY stack"
                )
            }
            lease = connection.execute(
                "SELECT expires_at FROM lease WHERE singleton = 1"
            ).fetchone()
            proposals = {
                row["stack"]: {"digest": row["digest"], "url": row["url"]}
                for row in connection.execute(
                    "SELECT stack, digest, url FROM pending_proposals ORDER BY stack"
                )
            }
        return UpdaterStatus(
            breaker_open=bool(breaker and breaker["is_open"]),
            breaker_reason=(breaker["reason"] if breaker and breaker["is_open"] else None),
            accepted_digests=digests,
            lease_active=bool(lease and datetime.fromisoformat(lease["expires_at"]) > now),
            pending_proposals=proposals,
        )

    def get_accepted_digest(self, stack: str) -> str | None:
        with self._connect() as connection:
            row = connection.execute(
                "SELECT digest FROM accepted_digests WHERE stack = ?", (stack,)
            ).fetchone()
        return row["digest"] if row else None

    def set_accepted_digest(self, stack: str, digest: str, now: datetime) -> None:
        with self._connect() as connection:
            connection.execute(
                """
                INSERT INTO accepted_digests(stack, digest, accepted_at) VALUES(?, ?, ?)
                ON CONFLICT(stack) DO UPDATE SET digest=excluded.digest, accepted_at=excluded.accepted_at
                """,
                (stack, digest, now.isoformat()),
            )
            connection.execute("DELETE FROM candidates WHERE stack = ?", (stack,))
            connection.execute(
                "DELETE FROM pending_proposals WHERE stack = ?", (stack,)
            )

    def get_pending_proposal(self, stack: str) -> PendingProposal | None:
        with self._connect() as connection:
            row = connection.execute(
                "SELECT digest, url, proposed_at FROM pending_proposals WHERE stack = ?",
                (stack,),
            ).fetchone()
        if row is None:
            return None
        return PendingProposal(
            digest=row["digest"],
            url=row["url"],
            proposed_at=datetime.fromisoformat(row["proposed_at"]),
        )

    def set_pending_proposal(
        self, stack: str, digest: str, url: str, now: datetime
    ) -> None:
        with self._connect() as connection:
            connection.execute(
                """
                INSERT INTO pending_proposals(stack, digest, url, proposed_at)
                VALUES(?, ?, ?, ?)
                ON CONFLICT(stack) DO UPDATE SET
                    digest=excluded.digest,
                    url=excluded.url,
                    proposed_at=excluded.proposed_at
                """,
                (stack, digest, url, now.isoformat()),
            )

    def clear_pending_proposal(self, stack: str) -> bool:
        with self._connect() as connection:
            cursor = connection.execute(
                "DELETE FROM pending_proposals WHERE stack = ?", (stack,)
            )
        return cursor.rowcount == 1

    def observe_candidate(
        self, stack: str, digest: str, now: datetime
    ) -> CandidateObservation:
        with self._connect() as connection:
            row = connection.execute(
                "SELECT * FROM candidates WHERE stack = ? AND digest = ?",
                (stack, digest),
            ).fetchone()
            if row:
                count = int(row["observation_count"]) + 1
                first_seen = datetime.fromisoformat(row["first_seen"])
                connection.execute(
                    "UPDATE candidates SET last_seen = ?, observation_count = ? WHERE stack = ? AND digest = ?",
                    (now.isoformat(), count, stack, digest),
                )
            else:
                count = 1
                first_seen = now
                connection.execute("DELETE FROM candidates WHERE stack = ?", (stack,))
                connection.execute(
                    "INSERT INTO candidates VALUES(?, ?, ?, ?, ?)",
                    (stack, digest, now.isoformat(), now.isoformat(), count),
                )
        return CandidateObservation(digest, first_seen, now, count)

    def record_attempt(
        self,
        stack: str,
        old_digest: str,
        new_digest: str,
        result: str,
        now: datetime,
        detail: str,
    ) -> None:
        with self._connect() as connection:
            connection.execute(
                "INSERT INTO attempts(stack, old_digest, new_digest, result, attempted_at, detail) VALUES(?, ?, ?, ?, ?, ?)",
                (stack, old_digest, new_digest, result, now.isoformat(), detail),
            )

    def open_breaker(self, reason: str, now: datetime) -> None:
        with self._connect() as connection:
            connection.execute(
                """
                INSERT INTO breaker(singleton, is_open, reason, changed_at, clear_reason)
                VALUES(1, 1, ?, ?, NULL)
                ON CONFLICT(singleton) DO UPDATE SET
                    is_open=1, reason=excluded.reason, changed_at=excluded.changed_at, clear_reason=NULL
                """,
                (reason, now.isoformat()),
            )

    def clear_breaker(self, reason: str, now: datetime) -> None:
        if not reason.strip():
            raise ValueError("a clear-breaker reason is required")
        with self._connect() as connection:
            connection.execute(
                """
                INSERT INTO breaker(singleton, is_open, reason, changed_at, clear_reason)
                VALUES(1, 0, NULL, ?, ?)
                ON CONFLICT(singleton) DO UPDATE SET
                    is_open=0, reason=NULL, changed_at=excluded.changed_at, clear_reason=excluded.clear_reason
                """,
                (now.isoformat(), reason.strip()),
            )


class JsonLogNotifier:
    _SECRET_MARKERS = (
        "token",
        "password",
        "api_key",
        "apikey",
        "secret",
        "authorization",
        "credential",
        "cookie",
    )

    @classmethod
    def _redact(cls, value: object) -> object:
        if isinstance(value, dict):
            return {
                key: (
                    "[redacted]"
                    if any(marker in str(key).lower() for marker in cls._SECRET_MARKERS)
                    else cls._redact(item)
                )
                for key, item in value.items()
            }
        if isinstance(value, (list, tuple)):
            return [cls._redact(item) for item in value]
        return value

    def emit(self, event: str, fields: dict[str, object]) -> None:
        safe_fields = self._redact(fields)
        if not isinstance(safe_fields, dict):  # fields is typed as a dictionary
            raise TypeError("notification fields must be a dictionary")
        print(
            json.dumps({"event": event, **safe_fields}, sort_keys=True, default=str),
            file=sys.stderr,
            flush=True,
        )


class SystemClock:
    def now(self) -> datetime:
        return datetime.now(UTC)

    def sleep(self, seconds: float) -> None:
        time.sleep(seconds)
