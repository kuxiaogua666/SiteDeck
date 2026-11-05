"""Confirm the running gateway actually serves the published trusted certificate."""
import socket
from pathlib import Path
import os
import ssl
import sys
import time

expected = int(sys.argv[1], 16)
host = os.environ.get("SITEDECK_SERVER_HOST")
if not host:
    host = (Path(__file__).resolve().parents[1] / "secrets/server-host").read_text().strip()
if not host:
    raise SystemExit("Missing private server-host setting")
context = ssl.create_default_context()
last_error = None
for attempt in range(20):
    try:
        with socket.create_connection(("127.0.0.1", 48763), timeout=3) as raw:
            with context.wrap_socket(raw, server_hostname=host) as connection:
                certificate = connection.getpeercert()
                if int(certificate["serialNumber"], 16) != expected:
                    raise RuntimeError("Gateway has not loaded the expected certificate")
        print("Gateway certificate verified: hostname/IP, trust chain and serial match")
        break
    except (OSError, RuntimeError) as error:
        last_error = error
        time.sleep(.25)
else:
    raise SystemExit(str(last_error))
