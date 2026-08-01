FROM python:3.12-slim-bookworm@sha256:d50fb7611f86d04a3b0471b46d7557818d88983fc3136726336b2a4c657aa30b

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

RUN groupadd --gid 1031 updater \
    && useradd --uid 1031 --gid 1031 --no-create-home --shell /usr/sbin/nologin updater

WORKDIR /app
COPY pyproject.toml ./
COPY src ./src
RUN python -m pip install --no-cache-dir .

USER 1031:1031
ENTRYPOINT ["nas-stack-updater"]
CMD ["--config", "/config/policy.yaml", "daemon"]
