from __future__ import annotations

import hashlib
import json

import yaml

from .adapters import parse_image_reference
from .models import (
    Mode,
    Policy,
    PortainerStack,
    ResultCode,
    RunReport,
    StackObservation,
    StackPolicy,
    StackResult,
    UpdaterStatus,
)
from .ports import Clock, HealthPort, NotifierPort, PortainerPort, RegistryPort, StateStore


class EligibilityError(ValueError):
    pass


class Updater:
    """Deep module owning one complete monitor or update transaction."""

    def __init__(
        self,
        policy: Policy,
        *,
        portainer: PortainerPort,
        registry: RegistryPort,
        health: HealthPort,
        state: StateStore,
        notifier: NotifierPort,
        clock: Clock,
    ) -> None:
        self.policy = policy
        self.portainer = portainer
        self.registry = registry
        self.health = health
        self.state = state
        self.notifier = notifier
        self.clock = clock

    def run(self, mode: Mode | None = None) -> RunReport:
        selected_mode = mode or self.policy.mode
        started = self.clock.now()
        lease_token = self.state.acquire_lease(
            started, self.policy.lease_ttl_seconds
        )
        if lease_token is None:
            return RunReport(
                selected_mode,
                started,
                self.clock.now(),
                (StackResult("*", ResultCode.BUSY, "another run holds the lease"),),
            )
        try:
            status = self.state.get_status(started)
            if selected_mode is Mode.APPLY and status.breaker_open:
                return RunReport(
                    selected_mode,
                    started,
                    self.clock.now(),
                    (
                        StackResult(
                            "*",
                            ResultCode.BREAKER_OPEN,
                            status.breaker_reason or "circuit breaker is open",
                        ),
                    ),
                    breaker_open=True,
                )
            username = self.portainer.current_username()
            if username != self.policy.expected_username:
                raise PermissionError(
                    f"Portainer key belongs to {username!r}, expected {self.policy.expected_username!r}"
                )
            visible = {stack.name: stack for stack in self.portainer.list_stacks()}
            results: list[StackResult] = []
            updates = 0
            for stack_policy in sorted(self.policy.stacks, key=lambda item: item.name):
                if not stack_policy.enabled:
                    continue
                stack = visible.get(stack_policy.name)
                if stack is None:
                    results.append(
                        StackResult(
                            stack_policy.name,
                            ResultCode.NOT_VISIBLE,
                            "stack is not visible to the automation user",
                        )
                    )
                    continue
                try:
                    observation = self._observe(stack_policy, stack)
                    result, changed = self._evaluate(
                        stack_policy,
                        observation,
                        selected_mode,
                        updates < self.policy.max_updates_per_run,
                    )
                    updates += int(changed)
                    results.append(result)
                except EligibilityError as error:
                    results.append(
                        StackResult(stack_policy.name, ResultCode.INELIGIBLE, str(error))
                    )
                except Exception as error:  # fail one stack closed; continue reporting
                    results.append(
                        StackResult(stack_policy.name, ResultCode.ERROR, str(error))
                    )
                    self._notify(
                        "stack_error", {"stack": stack_policy.name, "error": str(error)}
                    )
            finished = self.clock.now()
            final_status = self.state.get_status(finished)
            report = RunReport(
                selected_mode,
                started,
                finished,
                tuple(results),
                updates_applied=updates,
                breaker_open=final_status.breaker_open,
            )
            self._notify(
                "run_finished",
                {
                    "mode": selected_mode.value,
                    "updates": updates,
                    "breaker_open": final_status.breaker_open,
                    "result_count": len(results),
                },
            )
            return report
        finally:
            self.state.release_lease(lease_token)

    def status(self) -> UpdaterStatus:
        return self.state.get_status(self.clock.now())

    def clear_breaker(self, reason: str) -> UpdaterStatus:
        self.state.clear_breaker(reason, self.clock.now())
        self._notify("breaker_cleared", {"reason": reason})
        return self.status()

    def _observe(
        self, stack_policy: StackPolicy, stack: PortainerStack
    ) -> StackObservation:
        if stack.status != 1:
            raise EligibilityError("stack is not active")
        compose = self.portainer.get_stack_file(stack.id)
        parsed = yaml.safe_load(compose)
        if not isinstance(parsed, dict) or not isinstance(parsed.get("services"), dict):
            raise EligibilityError("Compose file has no services mapping")
        services = parsed["services"]
        service_names = tuple(sorted(str(name) for name in services))
        if service_names != tuple(sorted(stack_policy.expected_services)):
            raise EligibilityError(
                f"services changed: expected {stack_policy.expected_services}, found {service_names}"
            )
        if len(services) != 1:
            raise EligibilityError("MVP only supports one-service stacks")
        service_name = service_names[0]
        service = services[service_name]
        if not isinstance(service, dict) or not isinstance(service.get("image"), str):
            raise EligibilityError("the single service must have a literal image reference")
        try:
            image = parse_image_reference(service["image"])
        except ValueError as error:
            raise EligibilityError(str(error)) from error
        image_status = self.portainer.get_image_status(stack.id)
        if image_status not in {"updated", "outdated"}:
            raise EligibilityError(f"Portainer image status is {image_status!r}")
        digest = self.registry.resolve_digest(image)
        return StackObservation(
            stack=stack,
            compose=compose,
            compose_hash=self._text_hash(compose),
            env_hash=self._env_hash(stack.env),
            service_name=service_name,
            image=image,
            image_status=image_status,
            remote_digest=digest,
        )

    def _evaluate(
        self,
        stack_policy: StackPolicy,
        observation: StackObservation,
        mode: Mode,
        update_slot_available: bool,
    ) -> tuple[StackResult, bool]:
        now = self.clock.now()
        accepted = self.state.get_accepted_digest(stack_policy.name)
        if accepted is None:
            if observation.image_status != "updated":
                return (
                    StackResult(
                        stack_policy.name,
                        ResultCode.BASELINE_BLOCKED,
                        "an update is already pending; the running digest cannot be proven",
                        observation.remote_digest,
                    ),
                    False,
                )
            self.state.set_accepted_digest(
                stack_policy.name, observation.remote_digest, now
            )
            return (
                StackResult(
                    stack_policy.name,
                    ResultCode.BASELINED,
                    "recorded the current registry digest as the accepted baseline",
                    observation.remote_digest,
                ),
                False,
            )
        if observation.image_status == "updated" and observation.remote_digest == accepted:
            return (
                StackResult(
                    stack_policy.name,
                    ResultCode.UP_TO_DATE,
                    "running image matches the accepted digest",
                    accepted,
                ),
                False,
            )
        if observation.remote_digest == accepted:
            return (
                StackResult(
                    stack_policy.name,
                    ResultCode.INELIGIBLE,
                    "Portainer reports outdated but the registry digest has not changed",
                    accepted,
                ),
                False,
            )
        candidate = self.state.observe_candidate(
            stack_policy.name, observation.remote_digest, now
        )
        age = (now - candidate.first_seen).total_seconds()
        mature = candidate.count >= 2 and age >= self.policy.candidate_min_age_seconds
        candidate_result = StackResult(
            stack_policy.name,
            ResultCode.CANDIDATE,
            f"candidate observed {candidate.count} time(s), age {int(age)}s",
            observation.remote_digest,
        )
        if (
            mode is Mode.MONITOR
            or not stack_policy.auto_apply
            or not mature
            or not update_slot_available
        ):
            return candidate_result, False
        return self._apply(stack_policy, observation, accepted)

    def _apply(
        self, stack_policy: StackPolicy, observation: StackObservation, accepted: str
    ) -> tuple[StackResult, bool]:
        now = self.clock.now()
        current_stacks = {stack.name: stack for stack in self.portainer.list_stacks()}
        current = current_stacks.get(stack_policy.name)
        if current is None:
            return (
                StackResult(
                    stack_policy.name, ResultCode.DRIFTED, "stack disappeared before apply"
                ),
                False,
            )
        current_compose = self.portainer.get_stack_file(current.id)
        if (
            current.id != observation.stack.id
            or current.endpoint_id != observation.stack.endpoint_id
            or self._text_hash(current_compose) != observation.compose_hash
            or self._env_hash(current.env) != observation.env_hash
        ):
            return (
                StackResult(
                    stack_policy.name,
                    ResultCode.DRIFTED,
                    "stack identity, Compose, or environment changed before apply",
                ),
                False,
            )
        self._notify(
            "update_started",
            {
                "stack": stack_policy.name,
                "old_digest": accepted,
                "new_digest": observation.remote_digest,
            },
        )
        try:
            self.portainer.update_stack(
                current, current_compose, current.env, repull=True
            )
            if self._wait_for_health(stack_policy):
                return self._record_update_success(
                    stack_policy,
                    observation,
                    accepted,
                    "updated and passed functional health verification",
                )
            failure = "functional health check timed out"
        except TimeoutError as error:
            if self._wait_for_update_confirmation(stack_policy, current.id):
                return self._record_update_success(
                    stack_policy,
                    observation,
                    accepted,
                    "deployment response timed out, but image status and health proved success",
                )
            failure = f"update response timed out and deployment could not be proven: {error}"
        except Exception as error:
            failure = f"update failed: {error}"
        return self._rollback(stack_policy, observation, accepted, failure)

    def _record_update_success(
        self,
        stack_policy: StackPolicy,
        observation: StackObservation,
        accepted: str,
        detail: str,
    ) -> tuple[StackResult, bool]:
        self.state.set_accepted_digest(
            stack_policy.name, observation.remote_digest, self.clock.now()
        )
        self.state.record_attempt(
            stack_policy.name,
            accepted,
            observation.remote_digest,
            ResultCode.UPDATED.value,
            self.clock.now(),
            detail,
        )
        return (
            StackResult(
                stack_policy.name,
                ResultCode.UPDATED,
                detail,
                observation.remote_digest,
            ),
            True,
        )

    def _rollback(
        self,
        stack_policy: StackPolicy,
        observation: StackObservation,
        accepted: str,
        failure: str,
    ) -> tuple[StackResult, bool]:
        parsed = yaml.safe_load(observation.compose)
        parsed["services"][observation.service_name]["image"] = observation.image.pinned(
            accepted
        )
        rollback_compose = yaml.safe_dump(parsed, sort_keys=False)
        reason = f"{stack_policy.name}: {failure}"
        try:
            self.portainer.update_stack(
                observation.stack,
                rollback_compose,
                observation.stack.env,
                repull=True,
            )
            healthy = self._wait_for_health(stack_policy)
        except Exception as error:
            healthy = False
            reason = f"{reason}; rollback request failed: {error}"
        self.state.open_breaker(reason, self.clock.now())
        if healthy:
            code = ResultCode.ROLLED_BACK
            detail = f"{failure}; restored the accepted digest and opened the breaker"
        else:
            code = ResultCode.ROLLBACK_FAILED
            detail = f"{failure}; rollback health verification failed; breaker opened"
        self.state.record_attempt(
            stack_policy.name,
            accepted,
            observation.remote_digest,
            code.value,
            self.clock.now(),
            detail,
        )
        self._notify(
            "rollback_finished",
            {"stack": stack_policy.name, "result": code.value, "reason": reason},
        )
        return StackResult(stack_policy.name, code, detail, accepted), True

    def _wait_for_health(self, stack_policy: StackPolicy) -> bool:
        deadline = self.clock.now().timestamp() + self.policy.verification_timeout_seconds
        while True:
            if self.health.check(stack_policy.health):
                return True
            remaining = deadline - self.clock.now().timestamp()
            if remaining <= 0:
                return False
            self.clock.sleep(min(10, remaining))

    def _wait_for_update_confirmation(
        self, stack_policy: StackPolicy, stack_id: int
    ) -> bool:
        deadline = self.clock.now().timestamp() + self.policy.verification_timeout_seconds
        while True:
            healthy = self.health.check(stack_policy.health)
            try:
                image_status = self.portainer.get_image_status(stack_id)
            except Exception:
                image_status = None
            if healthy and image_status == "updated":
                return True
            remaining = deadline - self.clock.now().timestamp()
            if remaining <= 0:
                return False
            self.clock.sleep(min(10, remaining))

    def _notify(self, event: str, fields: dict[str, object]) -> None:
        try:
            self.notifier.emit(event, fields)
        except Exception:  # notifications must never alter update safety or results
            pass

    @staticmethod
    def _text_hash(value: str) -> str:
        return hashlib.sha256(value.encode()).hexdigest()

    @staticmethod
    def _env_hash(value: tuple[dict[str, str], ...]) -> str:
        entries = sorted(
            json.dumps(item, sort_keys=True, separators=(",", ":")) for item in value
        )
        encoded = json.dumps(entries, separators=(",", ":"))
        return hashlib.sha256(encoded.encode()).hexdigest()
