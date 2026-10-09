"""Restore a real encrypted archive using its older immutable application image."""

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parents[2]
client, project, host_env, archive_tag, forward_tag = sys.argv[1:]
client_dir = Path(client)
scratch = client_dir.parent / "backup-proof"
scratch.mkdir(mode=0o700)
shutil.copyfile(ROOT / "tests/proving-ground/backup-store.py", scratch / "backup-store.py")
values = dict(line.split("=", 1) for line in Path(host_env).read_text().splitlines())
env = os.environ | {
    "COMPOSE_PROJECT_NAME": project,
    "COMPOSE_FILE": os.pathsep.join(
        str(client_dir / name)
        for name in (
            "deploy/compose.yaml",
            "compose.yaml",
            "backup-proof.json",
        )
    ),
}


def compose(
    *args: str, extra_env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        ["docker", "compose", "--env-file", ".env", *args],
        cwd=client_dir,
        env=env | (extra_env or {}),
        capture_output=True,
        text=True,
    )
    if result.returncode:
        print(result.stdout, file=sys.stderr)
        print(result.stderr, file=sys.stderr)
        result.check_returncode()
    return result


def write_env() -> None:
    path = client_dir / ".env"
    path.write_text("".join(f"{name}={value}\n" for name, value in values.items()))
    path.chmod(0o600)


def database_url(component: str, database: str = "ddp") -> str:
    login = "postgres" if component == "POSTGRES" else f"ddp_{component.lower()}_login"
    key = "POSTGRES_PASSWORD" if component == "POSTGRES" else f"{component}_DATABASE_PASSWORD"
    return f"postgres://{login}:{values[key]}@postgres:5432/{database}?sslmode=disable"


def maintenance(
    *args: str, component: str = "POSTGRES", database: str = "ddp"
) -> dict[str, Any]:
    response = compose(
        "run",
        "--rm",
        "--no-deps",
        "-e",
        "DATABASE_URL",
        "maintenance",
        *args,
        "--json",
        extra_env={"DATABASE_URL": database_url(component, database)},
    )
    result: dict[str, Any] = json.loads(response.stdout)
    assert result["ok"], result
    return result["data"]


def scalar(query: str) -> str:
    return compose(
        "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "ddp", "-At", "-c", query
    ).stdout.strip()


def release_env(revision: str) -> dict[str, str]:
    return {
        "GITHUB_EVENT_NAME": "push",
        "GITHUB_REF": "refs/heads/main",
        "GITHUB_REF_PROTECTED": "true",
        "GITHUB_REPOSITORY": archive_tag.split(":")[0].removeprefix("ghcr.io/"),
        "GITHUB_SHA": revision,
    }


def git(*args: str) -> str:
    # Keep Git's atomic metadata replacements in the publisher's Linux filesystem
    # view. Mixing macOS checkouts and container reads can expose an empty index.
    result = host(
        "git",
        "-c",
        "core.hooksPath=/dev/null",
        "-c",
        "commit.gpgsign=false",
        "-c",
        "user.name=Restore fixture",
        "-c",
        "user.email=restore@example.test",
        *args,
    )
    assert result.returncode == 0, result.stderr
    return result.stdout.strip()


def build_image(tag: str, *, succeeds: bool = True) -> tuple[str, str]:
    for args in (["add", "--all"], ["commit", "-qm", "Synthetic restore image"]):
        git(*args)
    revision = git("rev-parse", "HEAD")
    git("push", "-q", "origin", "HEAD:refs/heads/main")
    repository = tag.split(":")[0]
    result = host(
        "bash",
        "bin/publish-image.sh",
        extra_env=release_env(revision),
    )
    print(result.stdout)
    print(result.stderr, file=sys.stderr)
    if not succeeds:
        assert result.returncode != 0 and "image revision mismatch" in result.stderr, result.stderr
        return revision, ""
    result.check_returncode()
    digest = result.stdout.splitlines()[-1].removeprefix("publish: ")
    assert digest.startswith(repository + "@sha256:"), digest
    return revision, digest


def assert_published(image: str, revision: str = "") -> None:
    # The production publisher has already promoted the exact multi-platform index.
    stable = archive_tag.split(":")[0] + ":stable"
    references = [stable]
    if revision:
        references.append(archive_tag.split(":")[0] + ":" + revision)
    for reference in references:
        result = host(
            "docker",
            "buildx",
            "imagetools",
            "inspect",
            reference,
            "--format",
            "{{.Manifest.Digest}}",
        )
        result.check_returncode()
        assert image.endswith("@" + result.stdout.strip()), result.stdout
    result = host("docker", "buildx", "imagetools", "inspect", stable, "--raw")
    result.check_returncode()
    manifests = json.loads(result.stdout)["manifests"]
    assert {
        item["platform"]["architecture"] for item in manifests if item["platform"]["os"] == "linux"
    } == {"amd64", "arm64"}
    assert (
        sum(
            item.get("annotations", {}).get("vnd.docker.reference.type") == "attestation-manifest"
            for item in manifests
        )
        == 2
    )


def host(
    *command: str, extra_env: dict[str, str] | None = None, input_text: str | None = None
) -> subprocess.CompletedProcess[str]:
    # Run the real Linux host procedure in the isolated daemon's host namespace.
    # Only synthetic deployment paths and Docker connection settings cross over.
    outer = {
        key: value
        for key, value in os.environ.items()
        if key
        not in (
            "DOCKER_HOST",
            "DOCKER_CONTEXT",
            "DOCKER_CONFIG",
            "DOCKER_TLS_VERIFY",
            "DOCKER_CERT_PATH",
            "DOCKER_TLS",
        )
    }
    outer |= json.loads(Path(os.environ["DDP_TEST_OUTER_DOCKER_ENV"]).read_text())
    host_name = os.environ["DDP_TEST_DOCKER_HOST_CONTAINER"]
    args = ["docker", "exec"]
    if input_text is not None:
        args.append("-i")
    args += [
        "-w",
        client,
        "-e",
        "DOCKER_HOST=unix:///var/run/docker.sock",
        "-e",
        "DOCKER_TLS_VERIFY=",
        "-e",
        "DOCKER_CONFIG=/tmp/ddp-updater-docker",
        "-e",
        f"COMPOSE_FILE={env['COMPOSE_FILE']}",
        "-e",
        f"COMPOSE_PROJECT_NAME={project}",
    ]
    for name, value in (extra_env or {}).items():
        args.extend(["-e", f"{name}={value}"])
    return subprocess.run(
        [*args, host_name, *command], env=outer, input=input_text, capture_output=True, text=True
    )


def write_source(path: Path, content: str) -> None:
    # Keep tracked-file changes in the same filesystem view as Git and Buildx.
    result = host("sh", "-c", 'cat > "$1"', "sh", str(path), input_text=content)
    assert result.returncode == 0, result.stderr


def update(*, succeeds: bool = True) -> dict[str, Any]:
    result = host("bash", "bin/update.sh")
    ownership = host(
        "chown",
        f"{os.getuid()}:{os.getgid()}",
        str(client_dir / ".env"),
        str(client_dir / ".ddp-release.json"),
    )
    ownership.check_returncode()
    assert (result.returncode == 0) == succeeds, result.stdout + result.stderr
    state: dict[str, Any] = json.loads((client_dir / ".ddp-release.json").read_text())
    assert not state["pending"], state
    assert (client_dir / ".env").stat().st_mode & 0o777 == 0o600
    assert not list(client_dir.glob(".ddp-update.*/")), "updater staging files remained"
    return state


subprocess.run(
    [
        "openssl",
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-days",
        "1",
        "-subj",
        "/CN=backup-store",
        "-addext",
        "subjectAltName=DNS:backup-store",
        "-keyout",
        str(scratch / "key.pem"),
        "-out",
        str(scratch / "certificate.pem"),
    ],
    check=True,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
identity_file = scratch / "recovery.txt"
subprocess.run(
    ["go", "run", "filippo.io/age/cmd/age-keygen", "-o", str(identity_file)],
    cwd=ROOT,
    check=True,
    stdout=subprocess.DEVNULL,
    stderr=subprocess.DEVNULL,
)
recipient = subprocess.check_output(
    ["go", "run", "filippo.io/age/cmd/age-keygen", "-y", str(identity_file)],
    cwd=ROOT,
    text=True,
).strip()
identity = next(line for line in identity_file.read_text().splitlines() if line.startswith("AGE-"))
values |= {
    "BACKUP_ACCESS_KEY_ID": "synthetic-backup-access",
    "BACKUP_SECRET_ACCESS_KEY": "synthetic-backup-secret",
    "BACKUP_AGE_IDENTITY": identity,
}
write_env()
certificate_mount = f"{scratch / 'certificate.pem'}:/fixture/backup-ca.pem:ro"
(scratch / "trust.pem").write_bytes(
    (scratch / "certificate.pem").read_bytes() + (client_dir.parent / "smtp.crt").read_bytes()
)
trust_mount = f"{scratch / 'trust.pem'}:/fixture/trust.pem:ro"
proof: dict[str, Any] = {
    "services": {
        "backup-store": {
            "image": "python:3.12-slim-trixie",
            "command": ["python", "/fixture/backup-store.py"],
            "environment": {
                "TLS_CERTIFICATE": "/fixture/backup-ca.pem",
                "TLS_PRIVATE_KEY": "/fixture/key.pem",
            },
            "volumes": [
                certificate_mount,
                f"{scratch / 'key.pem'}:/fixture/key.pem:ro",
                f"{scratch / 'backup-store.py'}:/fixture/backup-store.py:ro",
            ],
            "healthcheck": {
                "test": [
                    "CMD",
                    "python",
                    "-c",
                    "import ssl,urllib.request; "
                    "urllib.request.urlopen('https://backup-store:18443/healthz',"
                    "context=ssl.create_default_context(cafile='/fixture/backup-ca.pem'),timeout=3)",
                ],
                "interval": "1s",
                "timeout": "5s",
                "retries": 30,
            },
        },
        "scheduler": {
            "environment": {"SSL_CERT_FILE": "/fixture/trust.pem"},
            "volumes": [trust_mount],
        },
        "maintenance": {
            "environment": {"SSL_CERT_FILE": "/fixture/trust.pem"},
            "volumes": [trust_mount],
        },
    }
}
(client_dir / "backup-proof.json").write_text(json.dumps(proof))
compose("up", "-d", "--wait", "backup-store")

registry_path = client_dir / "ddp.yaml"
registry = registry_path.read_text()
assert registry.count("deploy:\n") == 1 and "  backup:\n" not in registry
write_source(
    registry_path,
    registry.replace(
        "deploy:\n",
        "deploy:\n  backup:\n    endpoint: https://backup-store:18443\n"
        f"    region: auto\n    bucket: synthetic-backups\n    recipient: {recipient}\n",
        1,
    ),
)
# The initialized tree is filesystem-only. Record this synthetic implementation
# before embedding its revision, just as a client records source before a release.
git("init", "-q", "-b", "main")
origin = scratch / "origin.git"
git("init", "-q", "--bare", str(origin))
for directory in (client, str(origin)):
    host("git", "config", "--global", "--add", "safe.directory", directory).check_returncode()
git("remote", "add", "origin", str(origin))
# The proof's host-only files contain credentials or absolute test paths.
host("sh", "-c", "printf 'backup-proof.json\\n' > .git/info/exclude").check_returncode()
archive_revision, archive_image = build_image(archive_tag)
env["DDP_IMAGE_DIGEST"] = archive_image
values["DDP_IMAGE_DIGEST"] = archive_image
write_env()
assert_published(archive_image, archive_revision)
backup = maintenance("backup", "run", "--image", archive_image, component="BACKUP")["backup"]
assert backup["revision"] == archive_revision and backup["image"] == archive_image
# Seed the first backup before enabling its scheduler. Scheduled backup polls
# then see current evidence and cannot race this fixture's manual archive.
compose("up", "-d", "--no-deps", "--wait", "api", "scheduler")

forward = client_dir / "migrations/app/20990101000000_restore_forward.sql"
write_source(
    forward,
    "CREATE TABLE app.restore_forward_probe(id integer);\n"
    "ALTER TABLE app.restore_forward_probe OWNER TO ddp_owner;\n",
)
forward_revision, forward_image = build_image(forward_tag)
env["DDP_IMAGE_DIGEST"] = forward_image
plan = maintenance(
    "deploy", "plan", "--current", archive_image, "--image", forward_image, component="OWNER"
)
assert plan["action"] == "apply" and plan["requires_backup"]
assert plan["revision"] == forward_revision
assert len(plan["pending_migrations"]) == 1
assert scalar("SELECT to_regclass('app.restore_forward_probe') IS NULL") == "t"
rejected = maintenance(
    "deploy",
    "plan",
    "--current",
    archive_image,
    "--image",
    forward_image,
    "--failed",
    forward_image,
    component="OWNER",
)
assert rejected["action"] == "rejected"
assert_published(forward_image, forward_revision)
state = update()
assert state["current"] == forward_image and state["previous"] == archive_image
assert scalar("SELECT count(*) FROM ops.backups WHERE status='succeeded'") == "2"
assert (
    scalar(
        "SELECT max(finished_at) < (SELECT applied_at FROM ddp.client_migrations "
        "WHERE id LIKE '20990101000000%') FROM ops.backups WHERE status='succeeded'"
    )
    == "t"
), "migration started before the fresh backup completed"
assert (
    json.loads(scalar("SELECT manifest FROM ops.backups ORDER BY started_at DESC LIMIT 1"))["image"]
    == archive_image
)
values["DDP_IMAGE_DIGEST"] = forward_image
assert f"DDP_IMAGE_DIGEST={forward_image}\n" in (client_dir / ".env").read_text()
assert not maintenance(
    "deploy", "plan", "--current", archive_image, "--image", forward_image, component="OWNER"
)["requires_backup"]
assert scalar("SELECT to_regclass('app.restore_forward_probe') IS NOT NULL") == "t"
observed = maintenance("deploy", "status", component="READONLY")["deployment"]
assert observed["image"] == forward_image and observed["status"] == "succeeded"
assert observed["previous_image"] == archive_image and observed["revision"] == forward_revision
maintenance("health", "evaluate", component="SCHEDULER")
metrics = compose(
    "exec",
    "-T",
    "api",
    "python",
    "-c",
    "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:9091/metrics',timeout=6).read().decode())",
).stdout
assert 'ddp_health_state{check="ddp:deployment",state="ok"} 1' in metrics

# A real image can pass preflight and migrations yet fail API startup. Its new
# table must survive rollback, while both services return to the previous digest.
broken_migration = client_dir / "migrations/app/20990101000001_restore_broken.sql"
write_source(
    broken_migration,
    "CREATE TABLE app.restore_broken_probe(id integer);\n"
    "ALTER TABLE app.restore_broken_probe OWNER TO ddp_owner;\n",
)
dockerfile = client_dir / "deploy/Dockerfile"
original_dockerfile = dockerfile.read_text()
model_file = client_dir / "models/mart/customers.sql"
original_model = model_file.read_text()
original_definition = scalar("SELECT pg_get_viewdef('mart.customers'::regclass)")
write_source(model_file, "SELECT id, name || ' rejected release' AS name FROM core.customers\n")
write_source(
    client_dir / "client/broken-startup.sh",
    '#!/bin/sh\nif [ "$1" = api ]; then exit 1; fi\nexec ddp "$@"\n',
)
write_source(
    dockerfile,
    original_dockerfile
    + "\nCOPY --chmod=755 client/broken-startup.sh /usr/local/bin/broken-startup\n"
    'ENTRYPOINT ["broken-startup"]\n',
)
broken_revision, broken_image = build_image(forward_tag + "-broken")
assert_published(broken_image, broken_revision)
state = update(succeeds=False)
assert state["current"] == forward_image and state["failed"] == broken_image
assert scalar("SELECT pg_get_viewdef('mart.customers'::regclass)") == original_definition
postgres_log = compose("logs", "--no-color", "postgres").stdout
assert 'CREATE OR REPLACE VIEW "mart"."customers" AS SELECT id, name ||' in postgres_log
assert scalar("SELECT to_regclass('app.restore_broken_probe') IS NOT NULL") == "t", (
    "rollback undid a migration"
)
assert scalar("SELECT count(*) FROM ops.backups WHERE status='succeeded'") == "3"
latest_backup = json.loads(
    scalar("SELECT manifest FROM ops.backups ORDER BY started_at DESC LIMIT 1")
)
assert latest_backup["image"] == forward_image and latest_backup["revision"] == forward_revision
observed = maintenance("deploy", "status", component="READONLY")["deployment"]
assert observed["status"] == "failed" and observed["phase"] == "startup"
assert observed["image"] == broken_image and observed["revision"] == broken_revision
for service in ("api", "scheduler"):
    container = compose("ps", "-q", service).stdout.strip()
    assert (
        subprocess.check_output(
            ["docker", "inspect", "--format", "{{.Config.Image}}", container], text=True
        ).strip()
        == forward_image
    )
maintenance("health", "evaluate", component="SCHEDULER")
assert scalar("SELECT state FROM ops.alert_state WHERE rule_ref='ddp:deployment'") == "failing"
assert (
    scalar("SELECT count(*) FROM ops.alerts WHERE rule_ref='ddp:deployment' AND kind='alert'")
    == "1"
)
count = scalar("SELECT count(*) FROM ops.deployments")
assert update()["failed"] == broken_image
assert scalar("SELECT count(*) FROM ops.deployments") == count, "retried a rejected digest"
assert scalar("SELECT count(*) FROM ops.backups WHERE status='succeeded'") == "3"
# Pull again from the registry to prove the updater did not move stable backward.
stable = archive_tag.split(":")[0] + ":stable"
subprocess.run(["docker", "pull", stable], check=True)
assert broken_image in json.loads(
    subprocess.check_output(
        ["docker", "image", "inspect", stable, "--format", "{{json .RepoDigests}}"], text=True
    )
)
print("Host updater: real stable pull, backup gate, startup rollback and rejected digest passed")

# A subsequent corrected release is accepted, clears the active failure and
# requires no additional backup because its migration is already present.
write_source(dockerfile, original_dockerfile)
write_source(model_file, original_model)
host("rm", str(client_dir / "client/broken-startup.sh")).check_returncode()
fixed_revision, fixed_image = build_image(forward_tag + "-fixed")
assert_published(fixed_image, fixed_revision)
state = update()
assert state["current"] == fixed_image and state["previous"] == forward_image
env["DDP_IMAGE_DIGEST"] = fixed_image
values["DDP_IMAGE_DIGEST"] = fixed_image
assert scalar("SELECT count(*) FROM ops.backups WHERE status='succeeded'") == "3"
observed = maintenance("deploy", "status", component="READONLY")["deployment"]
assert observed["status"] == "succeeded" and observed["revision"] == fixed_revision
maintenance("health", "evaluate", component="SCHEDULER")
assert scalar("SELECT state FROM ops.alert_state WHERE rule_ref='ddp:deployment'") == "ok"
assert (
    scalar("SELECT count(*) FROM ops.alerts WHERE rule_ref='ddp:deployment' AND kind='recovery'")
    == "1"
)

# Advance a real bare origin at the publisher's second remote read. The wrapper
# only schedules that external change; all Git and registry operations are real.
git("commit", "--allow-empty", "-qm", "New main during publication")
new_main = git("rev-parse", "HEAD")
git("checkout", "-q", "--detach", fixed_revision)
git_wrapper = scratch / "git-wrapper"
git_wrapper.mkdir()
(git_wrapper / "git").write_text(
    "#!/bin/sh\nset -eu\n"
    'if [ "$1" = ls-remote ]; then\n'
    '  if [ -f "$READ_MARKER" ]; then\n'
    '    /usr/bin/git push -q origin "$ADVANCE_SHA:refs/heads/main"\n'
    '  else touch "$READ_MARKER"; fi\n'
    'fi\nexec /usr/bin/git "$@"\n'
)
(git_wrapper / "git").chmod(0o755)
stale = host(
    "env",
    f"PATH={git_wrapper}:/usr/local/bin:/usr/bin:/bin",
    "bash",
    "bin/publish-image.sh",
    extra_env=release_env(fixed_revision)
    | {
        "READ_MARKER": str(scratch / "remote-read"),
        "ADVANCE_SHA": new_main,
    },
)
assert stale.returncode == 0 and "main has advanced" in stale.stderr, stale.stderr
assert "exporting manifest list" in stale.stderr, "stale guard skipped before building"
assert_published(fixed_image)
git("checkout", "-q", "--detach", new_main)

# A build whose embedded revision is wrong must never replace the good pointer.
assert "${REVISION}" in original_dockerfile
write_source(dockerfile, original_dockerfile.replace("${REVISION}", "dev"))
build_image(forward_tag + "-wrong-version", succeeds=False)
assert_published(fixed_image)
host(
    "chown", "-R", f"{os.getuid()}:{os.getgid()}", str(client_dir / ".git"), str(origin)
).check_returncode()
print("Publisher: both platforms, SHA tag, attestations, stale build and bad revision passed")

# The newer image cannot verify the older archive: its embedded migration is
# missing there. This proves the subsequent success requires the recorded image.
wrong_image = subprocess.run(
    [
        "docker",
        "compose",
        "--env-file",
        ".env",
        "run",
        "--rm",
        "--no-deps",
        "-e",
        "DATABASE_URL",
        "maintenance",
        "backup",
        "restore",
        backup["id"],
        "--verify",
        "--json",
    ],
    cwd=client_dir,
    env=env | {"DATABASE_URL": database_url("POSTGRES")},
    capture_output=True,
    text=True,
)
assert wrong_image.returncode != 0 and "read-only migration" in wrong_image.stdout

host_tmp = scratch / "host-tmp"
host_tmp.mkdir(mode=0o700)
env["TMPDIR"] = str(host_tmp)
app_ids = compose("ps", "-q", "api", "scheduler").stdout
assert len(app_ids.splitlines()) == 2
script = client_dir / "bin/verify-backup.sh"
assert script.is_file(), "initializer omitted the host procedure"
before = (client_dir / ".env").read_bytes()
for expected in ("succeeded", "not_due"):
    completed = subprocess.run(
        [str(script)], cwd=client_dir, env=env, capture_output=True, text=True, check=True
    )
    assert json.loads(completed.stdout)["data"]["status"] == expected, completed.stdout
    assert (client_dir / ".env").read_bytes() == before, "host procedure rewrote environment"
assert scalar("SELECT count(*) FROM ops.backup_restores WHERE status='succeeded'") == "1"
assert scalar("SELECT count(*) FROM pg_database WHERE datname LIKE 'ddp_restore_%'") == "0"
assert scalar("SELECT count(*) FROM app.restore_forward_probe") == "0"

scalar(
    "UPDATE ops.backup_restores SET finished_at=clock_timestamp()-interval '8 days' "
    "WHERE status='succeeded'"
)
values["BACKUP_AGE_IDENTITY"] = "invalid-synthetic-recovery-key"
write_env()
failed = subprocess.run([str(script)], cwd=client_dir, env=env, capture_output=True, text=True)
assert failed.returncode != 0, "timer hid a failed restore"
assert scalar("SELECT status FROM ops.backup_restores ORDER BY started_at DESC LIMIT 1") == "failed"
assert scalar("SELECT count(*) FROM pg_database WHERE datname LIKE 'ddp_restore_%'") == "0"
values["BACKUP_AGE_IDENTITY"] = identity
write_env()
recovered = subprocess.run(
    [str(script)], cwd=client_dir, env=env, capture_output=True, text=True, check=True
)
assert json.loads(recovered.stdout)["data"]["status"] == "succeeded"
assert scalar("SELECT count(*) FROM ops.backup_restores WHERE status='succeeded'") == "2"
assert scalar("SELECT count(*) FROM pg_database WHERE datname LIKE 'ddp_restore_%'") == "0"
# Running Python jobs share TMPDIR with backup and restore work.
staging = compose(
    "run",
    "--rm",
    "--no-deps",
    "--entrypoint",
    "sh",
    "maintenance",
    "-c",
    "find /backup-staging -mindepth 1 -maxdepth 1 "
    "\\( -name 'ddp-backup-*' -o -name 'ddp-restore-*' \\)",
).stdout.strip()
assert not staging, f"backup or restore staging remained:\n{staging}"
assert compose("ps", "-q", "api", "scheduler").stdout == app_ids, "services were recreated"
assert not list(host_tmp.iterdir()), "host verification files remained"
assert not subprocess.check_output(
    [
        "docker",
        "ps",
        "-aq",
        "--filter",
        f"label=com.docker.compose.project={project}",
        "--filter",
        "name=ddp-backup-verify-",
    ],
    text=True,
).strip(), "maintenance container remained"
print("Weekly restore: recorded image, encrypted archive, due guard and failure recovery passed")

# Rehearse the operator's retained recovery using the oldest real archive. The
# current image must advance it before Compose can use it. Preserve the original
# database and both host files, so a failed cutover can be reversed without loss.
compose("stop", "scheduler", "api")
host_state = (client_dir / ".ddp-release.json").read_bytes()
host_environment = (client_dir / ".env").read_bytes()
scalar("INSERT INTO app.restore_forward_probe VALUES (7)")
original_oid = scalar("SELECT oid FROM pg_database WHERE datname='ddp'")
env["DDP_IMAGE_DIGEST"] = archive_image
retained = maintenance(
    "backup", "restore", backup["id"],
    "--database", "recovered_client", "--confirm", "recovered_client",
)
assert retained["status"] == "succeeded" and retained["database"] == "recovered_client"
env["DDP_IMAGE_DIGEST"] = fixed_image
maintenance("migrate", "up", component="OWNER", database="recovered_client")
maintenance("models", "apply", component="SCHEDULER", database="recovered_client")
maintenance("models", "verify", component="SCHEDULER", database="recovered_client")


def cluster_sql(statement: str) -> str:
    return compose(
        "exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1",
        "-U", "postgres", "-d", "postgres", "-At", "-c", statement,
    ).stdout.strip()


restored_oid = cluster_sql("SELECT oid FROM pg_database WHERE datname='recovered_client'")
cluster_sql("ALTER DATABASE ddp ALLOW_CONNECTIONS false")
cluster_sql("ALTER DATABASE recovered_client ALLOW_CONNECTIONS false")
cluster_sql(
    "SELECT pg_terminate_backend(pid) FROM pg_stat_activity "
    "WHERE datname IN ('ddp','recovered_client')"
)
cluster_sql(
    "BEGIN; ALTER DATABASE ddp RENAME TO ddp_pre_recovery; "
    "ALTER DATABASE recovered_client RENAME TO ddp; "
    "ALTER DATABASE ddp ALLOW_CONNECTIONS true; COMMIT"
)
assert scalar("SELECT oid FROM pg_database WHERE datname='ddp'") == restored_oid
assert scalar("SELECT count(*) FROM app.restore_forward_probe") == "0"
assert scalar("SELECT to_regclass('app.restore_broken_probe') IS NOT NULL") == "t"
assert scalar("SELECT count(*) FROM mart.customers") == "2"
compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "api")
subprocess.run(
    ["npm", "exec", "--prefix", "frontend", "--workspace", "apps/portal", "--",
     "playwright", "test", "--reporter=line"],
    cwd=ROOT,
    env=os.environ | {
        "DDP_BASE_URL": "http://localhost:18081",
        "DDP_TEST_EMAIL": "operator@example.test",
        "DDP_TEST_PASSWORD": "synthetic-browser-password-2026",
    },
    check=True,
)
maintenance(
    "deploy", "record", "--current", fixed_image, "--image", fixed_image,
    "--revision", fixed_revision, "--status", "succeeded", "--phase", "ready",
    component="OWNER",
)
restored_deployment = maintenance("deploy", "status", component="READONLY")["deployment"]
assert restored_deployment["image"] == fixed_image
assert restored_deployment["revision"] == fixed_revision
assert restored_deployment["status"] == "succeeded"
assert (client_dir / ".env").read_bytes() == host_environment
assert (client_dir / ".ddp-release.json").read_bytes() == host_state

# Exercise the retained-original recovery before allowing scheduler side effects.
compose("stop", "api")
cluster_sql("ALTER DATABASE ddp ALLOW_CONNECTIONS false")
cluster_sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='ddp'")
cluster_sql(
    "BEGIN; ALTER DATABASE ddp RENAME TO ddp_recovery_attempt; "
    "ALTER DATABASE ddp_pre_recovery RENAME TO ddp; "
    "ALTER DATABASE ddp ALLOW_CONNECTIONS true; COMMIT"
)
assert scalar("SELECT oid FROM pg_database WHERE datname='ddp'") == original_oid
assert scalar("SELECT id FROM app.restore_forward_probe") == "7"
compose("up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "api", "scheduler")
assert (client_dir / ".env").read_bytes() == host_environment
assert (client_dir / ".ddp-release.json").read_bytes() == host_state
print("Retained recovery: old archive, current schema, portal cutover and reversal passed")
