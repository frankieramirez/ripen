from __future__ import annotations

import hashlib
import json

import yaml
from yaml.nodes import MappingNode, ScalarNode

from .adapters import parse_image_reference
from .models import (
    HealthPolicy,
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
                    observations = self._observe(stack_policy, stack)
                    for observation in observations:
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
    ) -> tuple[StackObservation, ...]:
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
        if stack_policy.services:
            running = self.portainer.get_service_image_digests(stack)
            if set(running) != set(service_names):
                raise EligibilityError(
                    "running Compose services do not match the reviewed policy"
                )
            observations: list[StackObservation] = []
            for service_policy in stack_policy.services:
                if not service_policy.enabled:
                    continue
                service = services[service_policy.name]
                if not isinstance(service, dict) or not isinstance(
                    service.get("image"), str
                ):
                    raise EligibilityError(
                        f"service {service_policy.name!r} must have a literal image reference"
                    )
                try:
                    image = parse_image_reference(service["image"])
                except ValueError as error:
                    raise EligibilityError(str(error)) from error
                remote_digest = self.registry.resolve_platform_digest(
                    image, os_name="linux", architecture="amd64"
                )
                running_digest = running[service_policy.name]
                observations.append(
                    StackObservation(
                        stack=stack,
                        compose=compose,
                        compose_hash=self._text_hash(compose),
                        env_hash=self._env_hash(stack.env),
                        service_name=service_policy.name,
                        image=image,
                        image_status=(
                            "updated" if running_digest == remote_digest else "outdated"
                        ),
                        remote_digest=remote_digest,
                        state_key=f"{stack_policy.name}/{service_policy.name}",
                        health=service_policy.health,
                        auto_apply=service_policy.auto_apply,
                        running_digest=running_digest,
                    )
                )
            return tuple(observations)
        if len(services) != 1 or stack_policy.health is None:
            raise EligibilityError("single-service policy does not match Compose")
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
        return (StackObservation(
            stack=stack,
            compose=compose,
            compose_hash=self._text_hash(compose),
            env_hash=self._env_hash(stack.env),
            service_name=service_name,
            image=image,
            image_status=image_status,
            remote_digest=digest,
            state_key=stack_policy.name,
            health=stack_policy.health,
            auto_apply=stack_policy.auto_apply,
        ),)

    def _evaluate(
        self,
        stack_policy: StackPolicy,
        observation: StackObservation,
        mode: Mode,
        update_slot_available: bool,
    ) -> tuple[StackResult, bool]:
        now = self.clock.now()
        accepted = self.state.get_accepted_digest(observation.state_key)
        if accepted is None:
            baseline = observation.running_digest or observation.remote_digest
            if observation.running_digest is not None:
                self.state.set_accepted_digest(observation.state_key, baseline, now)
                return (
                    StackResult(
                        observation.state_key,
                        ResultCode.BASELINED,
                        "recorded the proven running service digest as the accepted baseline",
                        baseline,
                    ),
                    False,
                )
            if observation.image_status != "updated":
                return (
                    StackResult(
                        observation.state_key,
                        ResultCode.BASELINE_BLOCKED,
                        "an update is already pending; the running digest cannot be proven",
                        observation.remote_digest,
                    ),
                    False,
                )
            self.state.set_accepted_digest(
                observation.state_key, observation.remote_digest, now
            )
            return (
                StackResult(
                    observation.state_key,
                    ResultCode.BASELINED,
                    "recorded the current registry digest as the accepted baseline",
                    observation.remote_digest,
                ),
                False,
            )
        if (
            observation.running_digest is not None
            and observation.running_digest != accepted
        ):
            return (
                StackResult(
                    observation.state_key,
                    ResultCode.DRIFTED,
                    "running service digest changed outside the updater",
                    observation.running_digest,
                ),
                False,
            )
        if observation.image_status == "updated" and observation.remote_digest == accepted:
            return (
                StackResult(
                    observation.state_key,
                    ResultCode.UP_TO_DATE,
                    "running image matches the accepted digest",
                    accepted,
                ),
                False,
            )
        if observation.remote_digest == accepted:
            return (
                StackResult(
                    observation.state_key,
                    ResultCode.INELIGIBLE,
                    "Portainer reports outdated but the registry digest has not changed",
                    accepted,
                ),
                False,
            )
        candidate = self.state.observe_candidate(
            observation.state_key, observation.remote_digest, now
        )
        age = (now - candidate.first_seen).total_seconds()
        mature = candidate.count >= 2 and age >= self.policy.candidate_min_age_seconds
        candidate_result = StackResult(
            observation.state_key,
            ResultCode.CANDIDATE,
            f"candidate observed {candidate.count} time(s), age {int(age)}s",
            observation.remote_digest,
        )
        if (
            mode is Mode.MONITOR
            or not observation.auto_apply
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
                    observation.state_key,
                    ResultCode.DRIFTED,
                    "stack disappeared before apply",
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
                    observation.state_key,
                    ResultCode.DRIFTED,
                    "stack identity, Compose, or environment changed before apply",
                ),
                False,
            )
        deploy_compose = current_compose
        repull = True
        if observation.running_digest is not None:
            running = self.portainer.get_service_image_digests(current)
            if running.get(observation.service_name) != accepted:
                return (
                    StackResult(
                        observation.state_key,
                        ResultCode.DRIFTED,
                        "running service digest changed before apply",
                    ),
                    False,
                )
            deploy_compose = self._replace_service_image(
                current_compose,
                observation.service_name,
                observation.image.original,
                observation.image.pinned(observation.remote_digest),
            )
            repull = False
            if not self._stack_health_once(stack_policy):
                return (
                    StackResult(
                        observation.state_key,
                        ResultCode.INELIGIBLE,
                        "multi-service stack health preflight failed",
                    ),
                    False,
                )
        self._notify(
            "update_started",
            {
                "stack": observation.state_key,
                "old_digest": accepted,
                "new_digest": observation.remote_digest,
            },
        )
        try:
            self.portainer.update_stack(
                current, deploy_compose, current.env, repull=repull
            )
            if self._wait_for_stack_health(stack_policy) and self._running_digest_is(
                observation, observation.remote_digest
            ):
                return self._record_update_success(
                    stack_policy,
                    observation,
                    accepted,
                    "updated and passed functional health verification",
                )
            failure = "functional health check timed out"
        except TimeoutError as error:
            if self._wait_for_update_confirmation(stack_policy, observation, current.id):
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
            observation.state_key, observation.remote_digest, self.clock.now()
        )
        self.state.record_attempt(
            observation.state_key,
            accepted,
            observation.remote_digest,
            ResultCode.UPDATED.value,
            self.clock.now(),
            detail,
        )
        return (
            StackResult(
                observation.state_key,
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
        rollback_compose = self._replace_service_image(
            observation.compose,
            observation.service_name,
            observation.image.original,
            observation.image.pinned(accepted),
        )
        reason = f"{observation.state_key}: {failure}"
        try:
            self.portainer.update_stack(
                observation.stack,
                rollback_compose,
                observation.stack.env,
                repull=observation.running_digest is None,
            )
            healthy = self._wait_for_stack_health(stack_policy) and self._running_digest_is(
                observation, accepted
            )
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
            observation.state_key,
            accepted,
            observation.remote_digest,
            code.value,
            self.clock.now(),
            detail,
        )
        self._notify(
            "rollback_finished",
            {"stack": observation.state_key, "result": code.value, "reason": reason},
        )
        return StackResult(observation.state_key, code, detail, accepted), True

    def _health_policies(self, stack_policy: StackPolicy) -> tuple[HealthPolicy, ...]:
        if stack_policy.services:
            return tuple(service.health for service in stack_policy.services)
        if stack_policy.health is None:
            raise EligibilityError("stack has no health policy")
        return (stack_policy.health,)

    def _stack_health_once(self, stack_policy: StackPolicy) -> bool:
        try:
            return all(
                self.health.check(policy)
                for policy in self._health_policies(stack_policy)
            )
        except Exception:
            return False

    def _wait_for_stack_health(self, stack_policy: StackPolicy) -> bool:
        deadline = self.clock.now().timestamp() + self.policy.verification_timeout_seconds
        while True:
            if self._stack_health_once(stack_policy):
                return True
            remaining = deadline - self.clock.now().timestamp()
            if remaining <= 0:
                return False
            self.clock.sleep(min(10, remaining))

    def _wait_for_update_confirmation(
        self,
        stack_policy: StackPolicy,
        observation: StackObservation,
        stack_id: int,
    ) -> bool:
        deadline = self.clock.now().timestamp() + self.policy.verification_timeout_seconds
        while True:
            try:
                healthy = self._stack_health_once(stack_policy)
                if observation.running_digest is None:
                    confirmed = self.portainer.get_image_status(stack_id) == "updated"
                else:
                    confirmed = self._running_digest_is(
                        observation, observation.remote_digest
                    )
            except Exception:
                healthy = False
                confirmed = False
            if healthy and confirmed:
                return True
            remaining = deadline - self.clock.now().timestamp()
            if remaining <= 0:
                return False
            self.clock.sleep(min(10, remaining))

    def _running_digest_is(
        self, observation: StackObservation, expected_digest: str
    ) -> bool:
        if observation.running_digest is None:
            return True
        try:
            running = self.portainer.get_service_image_digests(observation.stack)
        except Exception:
            return False
        return running.get(observation.service_name) == expected_digest

    def _notify(self, event: str, fields: dict[str, object]) -> None:
        try:
            self.notifier.emit(event, fields)
        except Exception:  # notifications must never alter update safety or results
            pass

    @staticmethod
    def _replace_service_image(
        compose: str,
        service_name: str,
        expected_image: str,
        replacement_image: str,
    ) -> str:
        """Replace one literal image scalar without reserializing the Compose file."""

        try:
            root = yaml.compose(compose)
        except yaml.YAMLError as error:
            raise EligibilityError("Compose YAML cannot be parsed safely") from error

        def value_for(mapping: object, key: str) -> object:
            if not isinstance(mapping, MappingNode):
                raise EligibilityError(f"Compose {key!r} parent is not a mapping")
            matches = [
                value
                for name, value in mapping.value
                if isinstance(name, ScalarNode) and name.value == key
            ]
            if len(matches) != 1:
                raise EligibilityError(
                    f"Compose must contain exactly one {key!r} mapping entry"
                )
            return matches[0]

        services = value_for(root, "services")
        service = value_for(services, service_name)
        image = value_for(service, "image")
        if not isinstance(image, ScalarNode) or image.value != expected_image:
            raise EligibilityError("target service image changed before replacement")
        if image.style not in {None, "'", '"'}:
            raise EligibilityError("target service image must be a literal scalar")
        start = image.start_mark.index
        end = image.end_mark.index
        if not isinstance(service, MappingNode) or not (
            service.start_mark.index <= start < end <= service.end_mark.index
        ):
            raise EligibilityError("aliased service images cannot be updated safely")
        replacement = json.dumps(replacement_image)
        return compose[:start] + replacement + compose[end:]

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
