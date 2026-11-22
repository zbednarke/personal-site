#!/usr/bin/env python3
"""Prepare a private Ember credential and provision the ESP32 over USB."""
import argparse
import getpass
import hashlib
import ipaddress
import os
from pathlib import Path
import secrets
import struct
import subprocess
import sys
import time


DEVICE_PATH = "/686/device/v1/turn"


def make_state(args):
    os.umask(0o077)
    args.directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    (args.directory / "device-token").write_text(secrets.token_hex(32))
    if args.lan_ip:
        address = str(ipaddress.IPv4Address(args.lan_ip))
        subprocess.run([
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "30",
            "-subj", f"/CN={address}", "-addext", f"subjectAltName=IP:{address}",
            "-keyout", str(args.directory / "server-key.pem"), "-out", str(args.directory / "server.pem"),
        ], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (args.directory / "endpoint").write_text(f"https://{address}:{args.lan_port}{DEVICE_PATH}")
    else:
        (args.directory / "endpoint").write_text("https://zachbednarke.com" + DEVICE_PATH)
    print("Prepared private device configuration.")


def token_digest(directory):
    return hashlib.sha256((directory / "device-token").read_bytes()).hexdigest()


def serve_lan(args):
    directory = args.directory.resolve()
    address = (directory / "endpoint").read_text().removeprefix("https://").split("/")[0]
    environment = os.environ | {
        "EMBER_DEV_ONLY": "1",
        "EMBER_DEVICE_LAN_ADDR": address,
        "EMBER_DEVICE_LAN_CERT": str(directory / "server.pem"),
        "EMBER_DEVICE_LAN_KEY": str(directory / "server-key.pem"),
        "EMBER_DEVICE_TOKEN_SHA256": token_digest(directory),
    }
    print("Ember LAN backend listening at " + address, flush=True)
    os.execve(args.binary.resolve(), [str(args.binary.resolve())], environment)


def provision(args):
    import serial

    endpoint = (args.directory / "endpoint").read_bytes()
    token = (args.directory / "device-token").read_bytes()
    certificate = args.directory / "server.pem"
    ca = certificate.read_bytes() if certificate.exists() else b""
    ssid = input("2.4 GHz Wi-Fi name: ").encode()
    password = getpass.getpass("Wi-Fi password: ").encode()
    if not 1 <= len(ssid) <= 32 or (password and not 8 <= len(password) <= 63):
        raise SystemExit("Enter a valid Wi-Fi name and password.")
    payload = b"".join(struct.pack("<H", len(value)) + value for value in (ssid, password, endpoint, token, ca))
    if len(payload) > 4096:
        raise SystemExit("Configuration exceeds the device's provisioning limit.")
    port = serial.Serial(port=None, baudrate=921600, timeout=.3, write_timeout=2, exclusive=True)
    port.port = args.port
    port.dtr = port.rts = False
    with port:
        time.sleep(1)
        port.reset_input_buffer()
        port.write(f"WIFI SET {len(payload)}\n".encode() + payload)
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            status = port.readline().strip()
            if status == b"WIFI CONFIG SAVED":
                break
            if status == b"WIFI CONFIG ERROR":
                raise SystemExit("The ESP32 rejected the configuration.")
        else:
            raise SystemExit("No provisioning acknowledgment. Check that Web Serial is disconnected.")
        print("Configuration saved. Waiting for the ESP32 to join Wi-Fi…", flush=True)
        time.sleep(2.5)
        for _ in range(25):
            port.write(b"WIFI STATUS\n")
            deadline = time.monotonic() + .6
            while time.monotonic() < deadline:
                if port.readline().strip() == b"WIFI READY":
                    print("Ember is connected and ready. Move the ESP32 USB cable to wall power.")
                    return
            time.sleep(.4)
    raise SystemExit("Settings were saved, but Ember did not reach Wi-Fi readiness. Check the 2.4 GHz credentials.")


parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("directory", type=Path, help="private local directory outside the repository")
sub = parser.add_subparsers(dest="action", required=True)
create = sub.add_parser("create")
create.add_argument("--lan-ip")
create.add_argument("--lan-port", type=int, default=8443)
serve = sub.add_parser("serve-lan")
serve.add_argument("--binary", required=True, type=Path)
flash = sub.add_parser("provision")
flash.add_argument("--port", default="/dev/cu.usbserial-0001")
sub.add_parser("digest")
args = parser.parse_args()
if args.action == "create":
    make_state(args)
elif args.action == "serve-lan":
    serve_lan(args)
elif args.action == "provision":
    if not sys.stdin.isatty():
        raise SystemExit("Run provisioning in an interactive terminal.")
    provision(args)
else:
    print(token_digest(args.directory))
