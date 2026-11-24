"""One synthetic database + private-file rehearsal. Run via qa-postgres.sh."""
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
from http.client import HTTPConnection
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import select
import shutil
import stat
import struct
import subprocess
import sys
import time
import zlib

ROOT = Path("/fixture")
BINARY = ROOT / "gomar"
PAIR = ROOT / "pair"
SOURCE_STATE = ROOT / "source-state"
RESTORED_STATE = ROOT / "restored-state"
SOCKET = ROOT / "socket"
DATA = ROOT / "data"
PORT = 55432
SOURCE_DB = "gomar_qa_source"
LATER_DB = "gomar_qa_later"
NEGATIVE_DB = "gomar_qa_negative"
RESTORED_DB = "gomar_qa_restored"
READER = "qa_restore_reader"
BASE = "/api/v1/entities/1"
OWNER = "qa-restore-owner"
OUTSIDER = "qa-restore-other-outlet"

SEED = """
INSERT INTO gomar.entities(id,name,cuit,active) VALUES (1,'Synthetic business','00000000000',true);
INSERT INTO gomar.outlets(id,entity_id,name,active)
  VALUES (1,1,'Synthetic outlet A',true),(2,1,'Synthetic outlet B',true);
UPDATE gomar.users SET staff=false,superuser=false;
INSERT INTO gomar.memberships(id,user_id,entity_id,role,active)
  SELECT CASE WHEN username='qa-restore-owner' THEN 1 ELSE 2 END,id,1,'administration',true
  FROM gomar.users;
INSERT INTO gomar.outlet_grants VALUES (1,1),(2,2);
INSERT INTO gomar.suppliers(id,entity_id,name,cuit,active)
  VALUES (1,1,'Synthetic supplier','30711111111',true);
"""
DOCUMENT = {
    "outlet_id": 1, "supplier_id": 1, "issuer_cuit": "30711111111", "tipo": "invoice",
    "letra": "A", "punto_venta": 3, "numero": 40, "moneda": "ARS", "fecha_emision": "2026-10-05",
    "fecha_vencimiento": "2026-10-31", "neto": "100.10", "iva": "21.02", "total": "121.12",
    "descripcion": "Synthetic document",
}
MAPPING = """
SELECT COALESCE(json_agg(r ORDER BY kind, id), '[]') FROM (
  SELECT 'document' AS kind,a.id,d.id AS parent,d.entity_id,d.outlet_id,
    a.storage_name,a.original_filename,a.mime_type,a.size,a.created_at AS written_at
  FROM gomar.document_attachments a JOIN gomar.documents d ON d.id=a.document_id
  UNION ALL
  SELECT 'receipt',p.id,p.id,p.entity_id,p.outlet_id,
    r.storage_name,r.original_filename,r.mime_type,r.size,r.uploaded_at
  FROM gomar.ap_receipts r JOIN gomar.ap_payments p ON p.id=r.payment_id
) r;
"""
MIGRATIONS = """
SELECT json_agg(s ORDER BY version) FROM
  (SELECT version,name,digest FROM gomar.schema_migrations) s;
"""


class Rejected(RuntimeError):
    """Expected pre-start rejection of a broken synthetic pair."""


def require(condition, message):
    if not condition:
        raise Rejected(message)


def check_dir(path):
    info = path.lstat()
    require(stat.S_ISDIR(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o700
            and info.st_uid == os.getuid(), "unsafe directory")


def private_bytes(path):
    try:
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as file:
            info = os.fstat(file.fileno())
            require(stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
                    and info.st_uid == os.getuid() and info.st_nlink == 1, "unsafe file")
            return file.read()
    except OSError as exc:
        raise Rejected("missing or unsafe file") from exc


def sha(data):
    return hashlib.sha256(data).hexdigest()


def binary_sha():
    with BINARY.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()


def png(pixel):
    # Distinct, deterministic, valid one-pixel PNGs; no external files.
    def chunk(tag, data):
        return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data))

    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">2I5B", 1, 1, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(b"\x00" + pixel)) + chunk(b"IEND", b""))


def ask(port, method, path, expected, cookies=None, body=None, content_type=None):
    headers = {}
    if cookies:
        headers["Cookie"] = "; ".join(f"{name}={value}" for name, value in cookies.items())
        if method != "GET":
            headers["X-CSRFToken"] = cookies["csrftoken"]
            headers["Origin"] = f"http://127.0.0.1:{port}"
    if content_type:
        headers["Content-Type"] = content_type
    client = HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        client.request(method, path, body, headers)
        reply = client.getresponse()
        require(reply.status == expected, f"{method} {path}: HTTP {reply.status}, expected {expected}")
        fields = reply.headers
        data = reply.read()
    finally:
        client.close()
    if cookies is not None:
        for value in fields.get_all("Set-Cookie", []):
            cookies.update({name: morsel.value for name, morsel in SimpleCookie(value).items()})
    return fields, data


def post(port, path, status, cookies, body):
    return json.loads(ask(port, "POST", path, status, cookies, json.dumps(body), "application/json")[1])


def login(port, username, password):
    cookies = {}
    ask(port, "GET", "/api/v1/auth/csrf", 200, cookies)
    post(port, "/api/v1/auth/login", 200, cookies, {"usuario": username, "clave": password})
    require(bool(cookies.get("sessionid")), "synthetic login did not establish a session")
    return cookies


def upload(port, path, cookies, filename, data):
    boundary = "gomar-fixture-" + secrets.token_hex(12)
    body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"archivo\"; filename=\"{filename}\"\r\n"
            "Content-Type: image/png\r\n\r\n").encode() + data + f"\r\n--{boundary}--\r\n".encode()
    return json.loads(ask(port, "POST", path, 201, cookies, body, f"multipart/form-data; boundary={boundary}")[1])


def download(port, path, cookies, filename, exact):
    headers, content = ask(port, "GET", path, 200, cookies)
    require(content == exact, "download bytes differ")
    require(headers.get("Content-Type") == "image/png"
            and headers.get("Content-Disposition") == f'attachment; filename="{filename}"'
            and headers.get("Content-Length") == str(len(exact))
            and headers.get("Cache-Control") == "private, no-store", "download metadata differs")


def denied(port, path, status, cookies, filename, data, storage_name):
    headers, body = ask(port, "GET", path, status, cookies)
    require(headers.get("Content-Type", "").startswith("application/json")
            and headers.get("Content-Disposition") is None
            and data not in body and filename.encode() not in body and storage_name.encode() not in body,
            "denial disclosed private download data")


class Fixture:
    def __init__(self, pg):
        self.pg = Path(pg)
        self.user = pwd.getpwuid(os.getuid()).pw_name
        # In particular, do not inherit credentials, DSNs, PG* or provider configuration.
        self.env = {key: os.environ[key] for key in ("PATH", "LD_LIBRARY_PATH", "LANG", "LC_ALL", "TZ")
                    if key in os.environ}
        self.env.update(HOME=str(ROOT / "home"), TMPDIR=str(ROOT / "tmp"))

    def run(self, *args, env=None, input=None):
        proc = subprocess.run(args, env=self.env if env is None else env, input=input,
                              text=True, capture_output=True, check=False, timeout=20)
        if proc.returncode:
            # Do not print arguments, subprocess output or synthetic secrets.
            raise RuntimeError(f"{Path(args[0]).name} failed (exit {proc.returncode}); no pass manifest")
        return proc.stdout.strip()

    def dsn(self, db, reader=False):
        return f"host={SOCKET} port={PORT} user={READER if reader else self.user} dbname={db} sslmode=disable"

    def psql(self, db, sql, reader=False):
        return self.run(self.pg / "psql", self.dsn(db, reader), "-XAtq", "-v", "ON_ERROR_STOP=1", input=sql)

    def createdb(self, db):
        self.run(self.pg / "createdb", "-h", SOCKET, "-p", str(PORT), "-U", self.user, db)
        require(self.psql(db, "SELECT to_regnamespace('gomar') IS NULL") == "t",
                "target was not a blank database")

    @contextmanager
    def postgres(self, *, initialize=False):
        if initialize:
            self.run(self.pg / "initdb", "-D", DATA, "--locale=C", "--encoding=UTF8", "--auth=trust")
        try:
            self.run(self.pg / "pg_ctl", "-D", DATA, "-l", ROOT / "postgres.log", "-w", "start",
                     "-o", f"-k {SOCKET} -p {PORT} -c listen_addresses=''")
            yield
        finally:
            if (DATA / "postmaster.pid").exists():
                self.run(self.pg / "pg_ctl", "-D", DATA, "-m", "fast", "-w", "stop")
            status = subprocess.run([self.pg / "pg_ctl", "-D", DATA, "status"], env=self.env,
                                    capture_output=True, check=False, timeout=5)
            require(status.returncode == 3 and not (DATA / "postmaster.pid").exists(),
                    "could not verify disposable PostgreSQL shutdown")

    def app_env(self, db, reader=False):
        return {**self.env, "GOMAR_POSTGRES_DSN": self.dsn(db, reader)}

    @contextmanager
    def app(self, db, state, reader=False):
        with (ROOT / "app.log").open("a") as log:
            proc = subprocess.Popen([BINARY, "-state", state, "-listen", "127.0.0.1:0"],
                                    env=self.app_env(db, reader), text=True,
                                    stdout=subprocess.PIPE, stderr=log)
            try:
                require(bool(select.select([proc.stdout], [], [], 10)[0]), "listener did not start")
                endpoint = re.fullmatch(r"http://127\.0\.0\.1:(\d+)\n", proc.stdout.readline())
                require(endpoint is not None, "no fixture loopback listener")
                port = int(endpoint[1])
                require(json.loads(ask(port, "GET", "/healthz", 200)[1]) == {"status": "ok"},
                        "health did not reach ready")
                yield port
            finally:
                exited = proc.poll() is not None
                if not exited:
                    proc.terminate()
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=5)
                    raise RuntimeError("fixture app did not stop normally") from None
                finally:
                    proc.stdout.close()
                require(not exited and proc.returncode == -15, "fixture app did not remain up until stopped")

    def quiesced(self):
        require(self.psql(SOURCE_DB, "SELECT count(*) FROM pg_stat_activity "
                         "WHERE datname=current_database() AND pid<>pg_backend_pid()") == "0",
                "source writer connections remain after stopping the sole app")

    def database_mapping(self, db):
        require(self.psql(db, "SELECT count(*) FROM gomar.intake_operations") == "0",
                "this fixture expects no staged intake")
        rows = json.loads(self.psql(db, MAPPING))
        names = set()
        for row in rows:
            pattern = r"document-[0-9a-f]{32}" if row["kind"] == "document" else r"[0-9a-f]{32}"
            name = row["storage_name"]
            require(re.fullmatch(pattern, name) and name not in names, "unsafe or duplicate storage basename")
            names.add(name)
        return rows

    def dump(self, archive):
        self.run(self.pg / "pg_dump", "--format=custom", "--no-owner", "--no-privileges",
                 "--file", archive, self.dsn(SOURCE_DB))
        return sha(private_bytes(archive))

    def restore(self, db, archive, digest):
        require(sha(private_bytes(archive)) == digest, "database dump digest mismatch")
        self.createdb(db)
        self.run(self.pg / "pg_restore", "--single-transaction", "--exit-on-error", "--no-owner",
                 "--no-privileges", "--dbname", self.dsn(db), archive)

    def restrict_reader(self):
        self.psql(RESTORED_DB, f"""
            CREATE ROLE {READER} LOGIN;
            ALTER ROLE {READER} IN DATABASE {RESTORED_DB} SET default_transaction_read_only = on;
            REVOKE TEMPORARY ON DATABASE {RESTORED_DB} FROM PUBLIC;
            REVOKE CREATE ON SCHEMA public FROM PUBLIC;
            GRANT USAGE ON SCHEMA gomar TO {READER};
            GRANT SELECT ON ALL TABLES IN SCHEMA gomar TO {READER};
        """)
        require(self.psql(RESTORED_DB, "SHOW transaction_read_only", reader=True) == "on",
                "restored app role is not read-only")
        require(self.psql(RESTORED_DB, """
            SELECT NOT rolsuper AND NOT rolcreaterole AND NOT rolcreatedb
            FROM pg_roles WHERE rolname=current_user;
            SELECT NOT EXISTS(SELECT 1 FROM pg_tables WHERE schemaname='gomar' AND
              has_table_privilege(current_user, format('%I.%I', schemaname, tablename),
                                  'INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER'));
        """, reader=True) == "t\nt", "restored app role can write fixture tables")


def inventory(state, rows):
    check_dir(state)
    require({entry.name for entry in state.iterdir()} == {"private"}, "unexpected state entry")
    private = state / "private"
    check_dir(private)
    require({entry.name for entry in private.iterdir()} == {r["storage_name"] for r in rows},
            "private file set differs")
    result = {}
    for row in rows:
        data = private_bytes(private / row["storage_name"])
        require(len(data) == row["size"], "private file size differs")
        result[row["storage_name"]] = sha(data)
    return result


def describe(document_id, attachment_id, payment_id):
    return [
        ("document", attachment_id, document_id, f"{BASE}/documents/{document_id}/attachments/{attachment_id}",
         "fixture-document.png", png(b"\x1c\xa0\x3a")),
        ("receipt", payment_id, payment_id, f"{BASE}/payments/{payment_id}/receipt",
         "fixture-receipt.png", png(b"\xbb\x36\x6d")),
    ]


def stage_source(fixture, commit, tree):
    PAIR.mkdir(mode=0o700)
    password = secrets.token_urlsafe(24)
    env = fixture.app_env(SOURCE_DB)
    fixture.createdb(SOURCE_DB)
    fixture.run(BINARY, "-state", SOURCE_STATE, "-init", env=env)
    for account in (OWNER, OUTSIDER):
        fixture.run(BINARY, "-state", SOURCE_STATE, "-create-admin", account, env=env, input=password + "\n")
    fixture.psql(SOURCE_DB, SEED)

    with fixture.app(SOURCE_DB, SOURCE_STATE) as port:
        source_owner = login(port, OWNER, password)
        restore_sessions = [login(port, account, password) for account in (OWNER, OUTSIDER)]
        document_id = post(port, f"{BASE}/documents", 201, source_owner, DOCUMENT)["id"]
        document = f"{BASE}/documents/{document_id}"
        image = upload(port, document + "/attachments", source_owner, "fixture-document.png", png(b"\x1c\xa0\x3a"))
        post(port, document + "/review", 200, source_owner, {"motivo": "Synthetic review", "expected_revision": 1})
        payable = post(port, document + "/payable", 201, source_owner, {"motivo": "Synthetic payable"})
        draft = post(port, f"{BASE}/payments", 201, source_owner, {
            "outlet_id": 1, "supplier_id": 1, "moneda": "ARS", "total": "60.00", "metodo": "cash",
        })
        payment = f"{BASE}/payments/{draft['id']}"
        post(port, payment + "/post", 200, source_owner, {
            "idempotency_key": draft["idempotency_key"], "aplicaciones": [
                {"payable_id": payable["id"], "importe": "60.00"},
            ],
        })
        upload(port, payment + "/receipt", source_owner, "fixture-receipt.png", png(b"\xbb\x36\x6d"))
        ids = [document_id, image["id"], draft["id"]]
        for _, _, _, path, filename, data in describe(*ids):
            download(port, path, source_owner, filename, data)
        cutoff = time.monotonic()

    fixture.quiesced()
    checkpoint = datetime.now(timezone.utc).isoformat(timespec="microseconds")
    rows = fixture.database_mapping(SOURCE_DB)
    source_inventory = inventory(SOURCE_STATE, rows)
    for (kind, row_id, parent, _, filename, data), row in zip(describe(*ids), rows, strict=True):
        require((row["kind"], row["id"], row["parent"], row["entity_id"], row["outlet_id"],
                 row["original_filename"], row["mime_type"], row["size"]) ==
                (kind, row_id, parent, 1, 1, filename, "image/png", len(data))
                and source_inventory[row["storage_name"]] == sha(data), "source HTTP-to-file mapping differs")

    digest = fixture.dump(PAIR / "checkpoint.dump")
    shutil.copytree(SOURCE_STATE, PAIR / "state")
    require(inventory(PAIR / "state", rows) == source_inventory == inventory(SOURCE_STATE, rows)
            and fixture.database_mapping(SOURCE_DB) == rows, "checkpoint changed during sealing")
    migrations = json.loads(fixture.psql(SOURCE_DB, MIGRATIONS))
    fixture.quiesced()
    snapshot_verified = time.monotonic()
    shutil.copytree(PAIR / "state", RESTORED_STATE)
    files_staged = time.monotonic()

    # A genuine later generation has an acknowledged extra attachment. The
    # older pair must omit it, and the two generations must not be mixable.
    with fixture.app(SOURCE_DB, SOURCE_STATE) as port:
        later_data = png(b"\x03\x61\xb2")
        later = upload(port, document + "/attachments", source_owner, "fixture-after-checkpoint.png", later_data)
        later_path = document + f"/attachments/{later['id']}"
        download(port, later_path, source_owner, "fixture-after-checkpoint.png", later_data)
    fixture.quiesced()
    later_rows = fixture.database_mapping(SOURCE_DB)
    newer = [r for r in later_rows if r not in rows]
    require(len(newer) == 1 and len(later_rows) == len(rows) + 1 and newer[0]["id"] == later["id"]
            and newer[0]["kind"] == "document", "later source generation did not advance")
    later_digest = fixture.dump(PAIR / "later.dump")
    (PAIR / "seal.json").write_text(json.dumps({
        "commit": commit, "tree": tree, "binary": binary_sha(), "migrations": migrations,
        "checkpoint_utc": checkpoint,
        "quiesce_to_snapshot_verification_seconds": round(snapshot_verified - cutoff, 3),
        "private_file_staging_seconds": round(files_staged - snapshot_verified, 3),
        "rows": rows, "files": source_inventory, "dump": digest, "later_dump": later_digest,
        "ids": ids, "after_checkpoint": [later_path, newer[0]["storage_name"]],
    }), encoding="utf-8")
    (PAIR / "sessions.json").write_text(json.dumps(restore_sessions), encoding="utf-8")

    for case in ("missing", "corrupt", "linked", "mode"):
        shutil.copytree(PAIR / "state", ROOT / case)
    name = rows[0]["storage_name"]
    (ROOT / "missing" / "private" / name).unlink()
    corrupt = ROOT / "corrupt" / "private" / name
    data = private_bytes(corrupt)
    corrupt.write_bytes(data[:-1] + bytes([data[-1] ^ 1]))
    link = ROOT / "linked" / "private" / name
    link.unlink()
    link.symlink_to(PAIR / "state" / "private" / name)
    (ROOT / "mode" / "private" / name).chmod(0o640)


def prove(fixture, commit, tree):
    state = RESTORED_STATE
    for path in (PAIR, state):
        require(os.statvfs(path).f_flag & os.ST_RDONLY, "proof started without read-only file mounts")
    seal = json.loads(private_bytes(PAIR / "seal.json"))
    require((seal["commit"], seal["tree"], seal["binary"]) == (commit, tree, binary_sha()),
            "binary or source identity differs from sealed pair")

    def reject(error, action):
        try:
            action()
        except Rejected as exc:
            require(str(exc) == error, f"negative check failed for a different reason: {exc}")
        else:
            raise RuntimeError(f"broken pair was admitted: {error}")

    def verified(db, candidate, next_step):
        require(fixture.database_mapping(db) == seal["rows"], "database-to-file mapping differs")
        require(json.loads(fixture.psql(db, MIGRATIONS)) == seal["migrations"], "migration ledger differs")
        require(inventory(candidate, seal["rows"]) == seal["files"], "private file digest differs")
        return next_step()

    def no_start():
        raise RuntimeError("negative case reached startup")

    reject("database dump digest mismatch", lambda: fixture.restore(LATER_DB, PAIR / "later.dump", seal["dump"]))
    fixture.restore(LATER_DB, PAIR / "later.dump", seal["later_dump"])
    reject("database-to-file mapping differs", lambda: verified(LATER_DB, state, no_start))

    fixture.restore(NEGATIVE_DB, PAIR / "checkpoint.dump", seal["dump"])
    for case, reason in (("missing", "private file set differs"), ("corrupt", "private file digest differs"),
                         ("linked", "missing or unsafe file"), ("mode", "unsafe file")):
        reject(reason, lambda: verified(NEGATIVE_DB, ROOT / case, no_start))

    # Files were staged separately. Time this import through the verified
    # downloads on their read-only mount, excluding the negative cases.
    import_start = time.monotonic()
    fixture.restore(RESTORED_DB, PAIR / "checkpoint.dump", seal["dump"])

    def serve():
        fixture.restrict_reader()
        fixture.run(BINARY, "-state", state, "-check", env=fixture.app_env(RESTORED_DB, reader=True))
        owner, outsider = json.loads(private_bytes(PAIR / "sessions.json"))
        with fixture.app(RESTORED_DB, state, reader=True) as port:
            for account, cookies in ((OWNER, owner), (OUTSIDER, outsider)):
                who = json.loads(ask(port, "GET", "/api/v1/auth/me", 200, cookies)[1])
                require(who == {"usuario": account}, "restored session identity differs")
            files = describe(*seal["ids"])
            for i, (_, _, _, path, filename, data) in enumerate(files):
                download(port, path, owner, filename, data)
                if i == 0:
                    first_download_seconds = time.monotonic() - import_start
            both_downloads_seconds = time.monotonic() - import_start
            for row, (_, _, _, path, filename, data) in zip(seal["rows"], files, strict=True):
                for status, cookies in ((401, None), (404, outsider)):
                    denied(port, path, status, cookies, filename, data, row["storage_name"])
            later_path, later_name = seal["after_checkpoint"]
            denied(port, later_path, 404, owner, "fixture-after-checkpoint.png", png(b"\x03\x61\xb2"), later_name)
        return first_download_seconds, both_downloads_seconds

    first, both = verified(RESTORED_DB, state, serve)
    return {
        "schema": 1, "kind": "isolated_private_restore_rehearsal", "commit": commit, "source_tree": tree,
        "checkpoint_utc": seal["checkpoint_utc"],
        "last_recovered_write": {"kind": "synthetic_payment_receipt", "utc": seal["rows"][1]["written_at"]},
        "quiesce_to_snapshot_verification_seconds": seal["quiesce_to_snapshot_verification_seconds"],
        "private_file_staging_seconds": seal["private_file_staging_seconds"],
        "database_import_with_prestaged_files": {
            "first_verified_download_seconds": round(first, 3),
            "both_verified_downloads_seconds": round(both, 3),
        },
        "checks": [
            "exact_document_and_payment_receipt_bytes_and_headers",
            "unauthenticated_and_other_outlet_denied_without_file",
            "post_checkpoint_attachment_absent",
            "rejected_before_start_mixed_archive_digest_and_restored_mapping",
            "rejected_before_start_missing_same_size_corrupt_linked_wrong_mode",
            "restored_app_read_only_database_and_files",
        ],
    }


def main():
    mode, pg, commit, tree = sys.argv[1:]
    os.umask(0o077)
    check_dir(ROOT)
    require(os.getuid() != 0 and Path("/").stat().st_uid == os.getuid()
            and {line.split(":")[0].strip() for line in Path("/proc/net/dev").read_text().splitlines()[2:]} == {"lo"},
            "rehearsal requires the isolated QA namespace")
    fixture = Fixture(pg)
    if mode == "seal":
        for path in (SOCKET, ROOT / "home", ROOT / "tmp"):
            path.mkdir(mode=0o700)
        with fixture.postgres(initialize=True):
            stage_source(fixture, commit, tree)
    elif mode == "prove":
        with fixture.postgres():
            summary = prove(fixture, commit, tree)
        (ROOT / "manifest.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    else:
        raise RuntimeError("run the synthetic rehearsal through scripts/qa-postgres.sh --private-restore")


if __name__ == "__main__":
    main()
