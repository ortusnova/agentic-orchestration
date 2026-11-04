"""The check that a preview answers at its address through the control
plane's front, before the kit comments it (workbench.preview.FrontChecker),
against a stand-in for Pomerium and the egress gate behind it: TLS that
requires a client certificate from the control plane's CA, a CONNECT for
the preview's name with a token, and the preview's answers inside."""

from __future__ import annotations

import asyncio
import datetime
import ipaddress
import ssl
from pathlib import Path

import pytest
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import ExtendedKeyUsageOID, NameOID

from workbench.preview import FrontChecker

TOKEN = "sortie-control-token-of-the-test"
ADDRESS = "http://t-1.preview.p1.ortus.example/"


def _name(cn: str) -> x509.Name:
    return x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, cn)])


class CA:
    def __init__(self, cn: str):
        self.key = ec.generate_private_key(ec.SECP256R1())
        now = datetime.datetime.now(datetime.timezone.utc)
        self.cert = (x509.CertificateBuilder().subject_name(_name(cn)).issuer_name(_name(cn))
                     .public_key(self.key.public_key()).serial_number(1)
                     .not_valid_before(now - datetime.timedelta(hours=1)).not_valid_after(now + datetime.timedelta(hours=1))
                     .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
                     .add_extension(x509.SubjectKeyIdentifier.from_public_key(self.key.public_key()), critical=False)
                     .sign(self.key, hashes.SHA256()))

    def write(self, path: Path) -> str:
        path.write_bytes(self.cert.public_bytes(serialization.Encoding.PEM))
        return str(path)

    def issue(self, tmp: Path, name: str, *, server: bool) -> tuple[str, str]:
        key = ec.generate_private_key(ec.SECP256R1())
        now = datetime.datetime.now(datetime.timezone.utc)
        b = (x509.CertificateBuilder().subject_name(_name(name)).issuer_name(self.cert.subject)
             .public_key(key.public_key()).serial_number(x509.random_serial_number())
             .not_valid_before(now - datetime.timedelta(hours=1)).not_valid_after(now + datetime.timedelta(hours=1)))
        b = b.add_extension(x509.AuthorityKeyIdentifier.from_issuer_public_key(self.key.public_key()), critical=False)
        if server:
            b = (b.add_extension(x509.SubjectAlternativeName([x509.IPAddress(ipaddress.ip_address("127.0.0.1"))]), critical=False)
                 .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.SERVER_AUTH]), critical=False))
        else:
            b = (b.add_extension(x509.SubjectAlternativeName([x509.DNSName(name)]), critical=False)
                 .add_extension(x509.ExtendedKeyUsage([ExtendedKeyUsageOID.CLIENT_AUTH]), critical=False))
        cert = b.sign(self.key, hashes.SHA256())
        crt, k = tmp / f"{name}.crt", tmp / f"{name}.key"
        crt.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
        k.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                        serialization.NoEncryption()))
        return str(crt), str(k)


class Front:
    """Pomerium and the egress gate as the check meets them: mTLS with the
    control plane's CA, CONNECT for a deployed preview's name on port 80
    with the token, then the preview's status for each path."""

    def __init__(self, tmp: Path):
        self.ca = CA("control plane CA")
        self.ca_file = self.ca.write(tmp / "ca.crt")
        self.cert, self.key = self.ca.issue(tmp, "front", server=True)
        self.p1_cert, self.p1_key = self.ca.issue(tmp, "p1", server=False)
        self.previews = {"t-1.preview.p1.ortus.example:80": {"/": 200, "/api/auth/mode": 200}}
        self.seen: list[tuple[str, str, str]] = []

    async def handle(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            head = await _head(reader)
            peer = dict(x[0] for x in writer.get_extra_info("peercert")["subject"])
            line, headers = head[0], dict(h.split(": ", 1) for h in head[1:])
            method, target, _ = line.split(" ")
            self.seen.append((peer["commonName"], line, headers.get("Proxy-Authorization", "")))
            if method != "CONNECT" or headers.get("Proxy-Authorization") != f"Bearer {TOKEN}":
                writer.write(b"HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
                return
            preview = self.previews.get(target)
            if preview is None:
                writer.write(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n")
                return
            writer.write(b"HTTP/1.1 200 Connection established\r\n\r\n")
            inner = await _head(reader)
            path = inner[0].split(" ")[1]
            self.seen.append(("inside", inner[0], dict(h.split(": ", 1) for h in inner[1:]).get("Host", "")))
            status = preview.get(path, 404)
            writer.write(f"HTTP/1.1 {status} X\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok".encode())
        finally:
            await writer.drain()
            writer.close()

    def check(self, address: str = ADDRESS, *, cert: str | None = None, key: str | None = None) -> tuple[str | None, list]:
        async def go():
            ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH, cafile=self.ca_file)
            ctx.load_cert_chain(self.cert, self.key)
            ctx.verify_mode = ssl.CERT_REQUIRED
            ctx.verify_flags &= ~ssl.VERIFY_X509_STRICT
            server = await asyncio.start_server(self.handle, "127.0.0.1", 0, ssl=ctx)
            port = server.sockets[0].getsockname()[1]
            try:
                checker = FrontChecker(f"https://127.0.0.1:{port}", cert or self.p1_cert, key or self.p1_key,
                                       self.ca_file, TOKEN, timeout=5)
                return await checker.answers(address)
            finally:
                server.close()

        return asyncio.run(go()), self.seen


async def _head(reader: asyncio.StreamReader) -> list[str]:
    lines = []
    while (line := (await reader.readline()).decode().rstrip("\r\n")):
        lines.append(line)
    return lines


@pytest.fixture
def front(tmp_path):
    return Front(tmp_path)


def test_a_preview_that_answers_through_the_front(front):
    err, seen = front.check()

    assert err is None
    # p1's own worker certificate, a CONNECT for the name's port 80 with the
    # control token, and the home and the auth mode inside, by name.
    connect = ("p1", "CONNECT t-1.preview.p1.ortus.example:80 HTTP/1.1", f"Bearer {TOKEN}")
    assert seen == [connect, ("inside", "GET / HTTP/1.1", "t-1.preview.p1.ortus.example"),
                    connect, ("inside", "GET /api/auth/mode HTTP/1.1", "t-1.preview.p1.ortus.example")]


def test_a_preview_whose_api_fails_does_not_answer(front):
    front.previews["t-1.preview.p1.ortus.example:80"]["/api/auth/mode"] = 502

    err, _ = front.check()

    assert err and "502" in err and "/api/auth/mode" in err


def test_a_name_the_gate_does_not_serve_does_not_answer(front):
    err, _ = front.check("http://t-2.preview.p1.ortus.example/")

    assert err and "404" in err and "CONNECT t-2.preview.p1.ortus.example:80" in err


def test_a_certificate_from_another_ca_does_not_get_through(front, tmp_path):
    (tmp_path / "other").mkdir()
    cert, key = CA("another CA").issue(tmp_path / "other", "p1", server=False)

    err, seen = front.check(cert=cert, key=key)

    assert err and seen == []


def test_a_front_that_is_down_does_not_answer(tmp_path):
    front = Front(tmp_path)
    checker = FrontChecker("https://127.0.0.1:9", front.p1_cert, front.p1_key, front.ca_file, TOKEN, timeout=5)

    err = asyncio.run(checker.answers(ADDRESS))

    assert err and ADDRESS in err
